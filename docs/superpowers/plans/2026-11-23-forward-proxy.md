# Proxy de salida en Go: plan de implementación

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Modo de trabajo (del diseño): mixto.** Cada tarea dice quién la escribe.
> En las tareas **Claude**, Claude hace todo. En las tareas **Tú**, Claude
> escribe las pruebas y se detiene. El usuario escribe el código guiándose por
> los pasos, avisa cuando termina, Claude revisa y después se hace el commit.
> Nunca escribas el código de una tarea **Tú** por el usuario.

**Goal:** Un proxy de salida HTTP en Go, solo con la librería estándar, que reenvía peticiones HTTP, abre túneles `CONNECT` para HTTPS y muestra en la terminal todo lo que pasa por él.

**Architecture:** Un solo paquete `main`. `net.Listen` acepta conexiones y cada cliente se atiende en su propia goroutine, que lee peticiones con `http.ReadRequest` desde un único `bufio.Reader`. `CONNECT` va al túnel (copia de bytes en las dos direcciones). Todo lo demás va al reenvío: conexión nueva al destino, petición reescrita, respuesta devuelta y keep-alive con el cliente.

**Tech Stack:** Go 1.27.4, librería estándar (`net`, `bufio`, `io`, `net/http` solo para analizar y escribir mensajes). En las pruebas: `testing`, `net/http/httptest`.

**Spec:** `docs/superpowers/specs/2026-11-23-forward-proxy-design.md`

## Global Constraints

- Go 1.27.4, módulo `github.com/yeferson59/goproxy`, un solo paquete `main` en la raíz del repo.
- Solo librería estándar, sin dependencias externas.
- En el código del proxy (no en pruebas), de `net/http` solo se usan `http.ReadRequest`, `http.ReadResponse`, `http.StatusText`, los tipos `Request`, `Response` y `Header`, y sus métodos `Write`. Prohibido: `http.Server`, `http.Transport`, `http.Client`, `httputil`.
- En las pruebas sí se permiten `net/http/httptest`, y `http.Client` o `http.Transport` como cliente.
- Bandera `-addr`, por defecto `127.0.0.1:8080`. Bandera `-v` para ver las cabeceras.
- Tiempos: conectar al destino, 10 s. Cliente inactivo entre peticiones, 60 s. Túneles sin límite.
- Errores: `400` (petición mal formada, URL no absoluta, esquema distinto de `http` o `CONNECT` sin puerto), `502` (destino inalcanzable), `504` (se agotó el tiempo al conectar).
- Después de cualquier respuesta de error, el proxy cierra la conexión con el cliente. (Decisión de Claude: puede quedar un cuerpo sin leer en el `bufio.Reader`, y leerlo como si fuera la siguiente petición sería peor.)
- Identificadores en inglés, comentarios en español. Los comentarios explican el porqué, no el qué.
- Al final: `go test -race ./...`, `go vet ./...` y `gofmt -l .` sin salida.

## Review Focus

1. **Respuesta del destino sin `Content-Length` ni `chunked`** (termina cerrando la conexión). El cliente recibe el cuerpo completo y luego el proxy cierra la conexión con él. Prueba: `TestForwardBodyWithoutLength` (Tarea 5).
2. **`Expect: 100-continue`** (curl lo manda con cuerpos de más de 1 KB). El cliente recibe la respuesta final, no el `100 Continue` del destino. Prueba: `TestForwardSkips100Continue` (Tarea 5).
3. **`HEAD`**: la respuesta trae `Content-Length` pero no trae cuerpo. El proxy no se queda esperando bytes y la conexión sirve para la siguiente petición. Prueba: `TestForwardHEADKeepsConnection` (Tarea 5).
4. **El cliente pide `Connection: close`**. El proxy cierra después de responder, sin esperar los 60 s. Prueba: `TestForwardClientConnectionClose` (Tarea 5).
5. **En un túnel, el servidor cierra primero**. El cliente ve EOF, las dos copias terminan cuando el cliente cierra, y se registra `cerrado` con los bytes correctos. Prueba: `TestTunnelServerClosesFirst` (Tarea 6).

---

## Archivos

| Archivo | Quién | Responsabilidad |
|---------|-------|-----------------|
| `logging.go` | Claude | Formato de las líneas de log |
| `proxy.go` | Claude | Tipo `Proxy`, valores por defecto, conexión al destino, error → código HTTP |
| `main.go` | Claude | Banderas, listener, Ctrl+C |
| `conn.go` | **Tú** | Bucle de `Accept`, bucle por conexión, lectura de peticiones, respuestas de error |
| `forward.go` | **Tú** | Cabeceras de salto y reenvío HTTP |
| `tunnel.go` | **Tú** | Túnel `CONNECT` |
| `helpers_test.go` | Claude | Utilidades compartidas de prueba |
| `logging_test.go`, `proxy_test.go`, `conn_test.go`, `forward_test.go`, `tunnel_test.go` | Claude | Pruebas de cada archivo |

---

### Tarea 1 (Claude): Logger

**Files:**
- Create: `logging.go`
- Test: `logging_test.go`

**Interfaces:**
- Produces:
  - `type Logger struct { mu sync.Mutex; out io.Writer; now func() time.Time }`
  - `func NewLogger(out io.Writer) *Logger` (`now = time.Now`)
  - `func (l *Logger) HTTP(method, target string, status int, elapsed time.Duration)`
  - `func (l *Logger) HTTPError(method, target string, status int, err error)`
  - `func (l *Logger) TunnelOpen(target string)`
  - `func (l *Logger) TunnelClosed(target string, up, down int64, elapsed time.Duration)`
  - `func (l *Logger) Headers(title string, h http.Header)`
  - `func (l *Logger) Errorf(format string, args ...any)`
  - `func formatBytes(n int64) string`, `func formatDuration(d time.Duration) string`

- [ ] **Paso 1: Escribir las pruebas que fallan** en `logging_test.go`

```go
func newTestLogger() (*Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	l := NewLogger(&buf)
	l.now = func() time.Time { return time.Date(2026, 11, 23, 14, 2, 11, 0, time.Local) }
	return l, &buf
}

func TestFormatBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0B", 512: "512B", 49152: "48KB", 1258291: "1.2MB"} {
		if got := formatBytes(n); got != want { t.Errorf("formatBytes(%d) = %q, quiero %q", n, got, want) }
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[time.Duration]string{0: "0ms", 12 * time.Millisecond: "12ms", 999 * time.Millisecond: "999ms", 3100 * time.Millisecond: "3.1s"}
	// misma forma que TestFormatBytes
}

func TestLoggerLines(t *testing.T) {
	cases := []struct{ name string; log func(*Logger); want string }{
		{"http", func(l *Logger) { l.HTTP("GET", "http://example.com/", 200, 12*time.Millisecond) },
			"14:02:11 GET     http://example.com/       200  12ms\n"},
		{"http error", func(l *Logger) { l.HTTPError("GET", "http://nohay.invalid/", 502, errors.New("dial: no such host")) },
			"14:02:11 GET     http://nohay.invalid/     502  dial: no such host\n"},
		{"tunnel open", func(l *Logger) { l.TunnelOpen("www.google.com:443") },
			"14:02:11 CONNECT www.google.com:443        túnel abierto\n"},
		{"tunnel closed", func(l *Logger) { l.TunnelClosed("www.google.com:443", 49152, 1258291, 3100*time.Millisecond) },
			"14:02:11 CONNECT www.google.com:443        cerrado  48KB↑ 1.2MB↓  3.1s\n"},
		{"error", func(l *Logger) { l.Errorf("petición mal formada de %s", "127.0.0.1:5000") },
			"14:02:11 ERROR   petición mal formada de 127.0.0.1:5000\n"},
	}
	// para cada caso: logger nuevo, llamar log, comparar buf.String() con want
}

func TestLoggerHeaders(t *testing.T) {
	l, buf := newTestLogger()
	l.Headers("petición del cliente", http.Header{"User-Agent": {"curl/8.0"}, "Accept": {"*/*"}, "X-Multi": {"a", "b"}})
	want := "    --- petición del cliente ---\n    Accept: */*\n    User-Agent: curl/8.0\n    X-Multi: a\n    X-Multi: b\n"
	// buf.String() == want
}
```

- [ ] **Paso 2: Correr y ver que fallan**

Run: `go test -run 'TestFormat|TestLogger' .`
Expected: FAIL de compilación (`undefined: NewLogger`).

- [ ] **Paso 3: Implementar `logging.go`**

Cada evento escribe una línea con `fmt.Fprintf(l.out, "%s %-7s %-25s %s\n", hora, método, destino, resto)`, donde la hora va en formato `"15:04:05"`. El resto es:
- `HTTP`: `"%d  %s"` (código y duración).
- `HTTPError`: `"%d  %v"` (código y error).
- `TunnelOpen`: `"túnel abierto"`.
- `TunnelClosed`: `"cerrado  %s↑ %s↓  %s"`.

`Errorf` usa `"%s %-7s %s\n"` con `"ERROR"`. `Headers` ordena las claves y escribe una línea por valor, con 4 espacios de sangría. Toda escritura se hace con `l.mu` tomado.

Formatos auxiliares:
- `formatBytes`: menos de 1024 → `"%dB"`; menos de 1 MiB → `"%dKB"` (división entera); si no → `"%.1fMB"`.
- `formatDuration`: menos de 1 s → `"%dms"` (`d.Milliseconds()`); si no → `"%.1fs"`.

- [ ] **Paso 4: Correr y ver que pasan**

Run: `go test -run 'TestFormat|TestLogger' .`
Expected: `ok`

- [ ] **Paso 5: Commit**

```bash
git add logging.go logging_test.go
git commit -m "Logger con el formato de líneas del proxy"
```

---

### Tarea 2 (Claude): Esqueleto: tipo Proxy, `main.go`, stubs y utilidades de prueba

**Files:**
- Create: `proxy.go`, `main.go`, `conn.go` (stubs), `forward.go` (stubs), `tunnel.go` (stub), `helpers_test.go`
- Test: `proxy_test.go`

**Interfaces:**
- Consumes: `NewLogger` (Tarea 1).
- Produces (implementado):
  - `type Proxy struct { Log *Logger; Verbose bool; DialTimeout time.Duration; IdleTimeout time.Duration; Dial func(addr string) (net.Conn, error) }`
  - `func NewProxy(log *Logger) *Proxy` (`DialTimeout = 10 * time.Second`, `IdleTimeout = 60 * time.Second`)
  - `func (p *Proxy) dial(addr string) (net.Conn, error)`: usa `p.Dial` si no es nil; si no, `net.DialTimeout("tcp", addr, p.DialTimeout)`.
  - `func statusForDialError(err error) int`: `504` si `errors.As(err, &netErr) && netErr.Timeout()`; si no, `502`.
- Produces (stubs con comentario de documentación en español que dice qué hacer, y cuerpo `panic("sin implementar: Tarea N")`):
  - `conn.go` (Tarea 3): `func (p *Proxy) Serve(ln net.Listener) error`, `func (p *Proxy) handleConn(client net.Conn)`, `func readRequest(br *bufio.Reader) (*http.Request, error)`, `func writeError(w io.Writer, status int, msg string) error`
  - `forward.go` (Tareas 4 y 5): `func removeHopByHop(h http.Header)`, `func (p *Proxy) forward(client net.Conn, req *http.Request) (keepAlive bool)`
  - `tunnel.go` (Tarea 6): `func (p *Proxy) tunnel(client net.Conn, br *bufio.Reader, req *http.Request)`
- Produces (`helpers_test.go`):
  - `type syncBuffer` (un `bytes.Buffer` con mutex; métodos `Write` y `String`)
  - `func startProxy(t *testing.T, configure func(*Proxy)) (addr string, logs *syncBuffer)`: escucha en `127.0.0.1:0`, `NewProxy` con `DialTimeout = 2s` e `IdleTimeout = 5s`, aplica `configure` si no es nil, corre `go p.Serve(ln)` y cierra el listener en `t.Cleanup`.
  - `func proxyClient(t *testing.T, proxyAddr string, base *http.Transport) *http.Client`: clona `base` (o uno nuevo si es nil), pone `Proxy = http.ProxyURL("http://"+proxyAddr)` y `Timeout: 5s`.
  - `func rawConn(t *testing.T, addr string) (net.Conn, *bufio.Reader)`: deadline de 5 s; se cierra en `Cleanup`.
  - `func closedAddr(t *testing.T) string`: escucha en `127.0.0.1:0`, cierra y devuelve la dirección.
  - `func waitForLog(t *testing.T, logs *syncBuffer, substr string)`: reintenta hasta 2 s y falla con el log completo si no aparece.
  - `func readBody(t *testing.T, r io.ReadCloser) string`
  - `func expectClosed(t *testing.T, br *bufio.Reader)`: lee un byte y exige `io.EOF`.

- [ ] **Paso 1: Escribir las pruebas que fallan** en `proxy_test.go`

```go
func TestNewProxyDefaults(t *testing.T) {
	log := NewLogger(io.Discard)
	p := NewProxy(log)
	// p.Log == log, p.DialTimeout == 10*time.Second, p.IdleTimeout == 60*time.Second, !p.Verbose, p.Dial == nil
}

func TestStatusForDialError(t *testing.T) {
	timeout := &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	dns := &net.DNSError{Err: "no such host", Name: "nohay.invalid", IsNotFound: true}
	// statusForDialError(timeout) == 504
	// statusForDialError(dns) == 502
	// statusForDialError(errors.New("connection refused")) == 502
}

func TestDialUsesInjectedFunc(t *testing.T) {
	sentinel := errors.New("dial de prueba")
	p := NewProxy(NewLogger(io.Discard))
	p.Dial = func(addr string) (net.Conn, error) { return nil, sentinel }
	// _, err := p.dial("x:1"); errors.Is(err, sentinel)
}

func TestDialDefault(t *testing.T) {
	// net.Listen en 127.0.0.1:0; p.dial(ln.Addr().String()) sin error
}
```

- [ ] **Paso 2: Correr y ver que fallan**

Run: `go test -run 'TestNewProxy|TestStatusForDialError|TestDial' .`
Expected: FAIL de compilación (`undefined: NewProxy`).

- [ ] **Paso 3: Implementar `proxy.go`, los stubs, `helpers_test.go` y `main.go`**

`main.go`:
1. Banderas `-addr` (`"127.0.0.1:8080"`, "dirección donde escucha el proxy") y `-v` ("muestra las cabeceras de cada petición y respuesta").
2. `net.Listen`; si falla, se imprime en stderr y se sale con `os.Exit(1)`.
3. `p := NewProxy(NewLogger(os.Stdout)); p.Verbose = *v`.
4. Una goroutine espera `os.Interrupt` o `syscall.SIGTERM` (`signal.Notify`) y cierra el listener.
5. Imprime `goproxy escuchando en <addr>`, llama a `p.Serve(ln)`, y si devuelve error, lo imprime y sale con código 1.

Cada stub lleva un comentario que resume lo que tiene que hacer según la tarea correspondiente.

- [ ] **Paso 4: Correr y ver que pasan**

Run: `go build ./... && go vet ./... && go test -run 'TestNewProxy|TestStatusForDialError|TestDial' .`
Expected: compila, `vet` sin salida, `ok`.

- [ ] **Paso 5: Commit**

```bash
git add proxy.go proxy_test.go main.go conn.go forward.go tunnel.go helpers_test.go
git commit -m "Esqueleto del proxy: tipo Proxy, main, stubs y utilidades de prueba"
```

---

### Tarea 3 (Tú): Aceptar conexiones y atender cada cliente

**Files:**
- Modify: `conn.go` (reemplazar los stubs)
- Test: `conn_test.go` (lo escribe Claude)

**Interfaces:**
- Consumes: `Proxy`, `p.Log`, `p.IdleTimeout` (Tarea 2); `p.forward`, `p.tunnel` (stubs por ahora).
- Produces: `Serve`, `handleConn`, `readRequest`, `writeError` con las firmas de la Tarea 2.

- [ ] **Paso 1 (Claude): Escribir las pruebas que fallan** en `conn_test.go`

```go
func TestWriteError(t *testing.T) {
	var buf bytes.Buffer
	writeError(&buf, 502, "no se pudo conectar")
	if !strings.HasPrefix(buf.String(), "HTTP/1.1 502 Bad Gateway\r\n") { t.Fatalf("línea de estado: %q", buf.String()) }
	resp, err := http.ReadResponse(bufio.NewReader(&buf), nil)
	// err == nil; resp.StatusCode == 502; resp.Close == true
	// resp.Header.Get("Content-Type") == "text/plain; charset=utf-8"
	// resp.ContentLength == int64(len("no se pudo conectar")); readBody == "no se pudo conectar"
}

func TestServeReturnsWhenListenerCloses(t *testing.T) {
	// ln en 127.0.0.1:0; done := make(chan error, 1); go func() { done <- p.Serve(ln) }()
	// ln.Close(); en menos de 2 s, <-done == nil
}

func TestMalformedRequestGets400(t *testing.T) {
	addr, logs := startProxy(t, nil)
	conn, br := rawConn(t, addr)
	io.WriteString(conn, "esto no es http\r\n\r\n")
	resp, err := http.ReadResponse(br, nil)
	// err == nil; resp.StatusCode == 400; readBody(...)
	expectClosed(t, br)
	waitForLog(t, logs, "ERROR")
}

func TestIdleClientIsClosed(t *testing.T) {
	addr, _ := startProxy(t, func(p *Proxy) { p.IdleTimeout = 200 * time.Millisecond })
	_, br := rawConn(t, addr)
	start := time.Now()
	expectClosed(t, br)
	// time.Since(start) < 2*time.Second
}

func TestClientHangUpIsQuiet(t *testing.T) {
	addr, logs := startProxy(t, nil)
	conn, _ := rawConn(t, addr)
	conn.Close()
	time.Sleep(200 * time.Millisecond)
	// !strings.Contains(logs.String(), "ERROR")  — cerrar sin mandar nada no es un error
}
```

- [ ] **Paso 2: Correr y ver que fallan**

Run: `go test -run 'TestWriteError|TestServe|TestMalformed|TestIdle|TestClientHangUp' .`
Expected: FAIL con `panic: sin implementar: Tarea 3`.

- [ ] **Paso 3 (Tú): Implementar `writeError(w io.Writer, status int, msg string) error`**

Escribe la respuesta a mano, byte por byte, como viaja por el cable:
- La línea de estado `HTTP/1.1 <código> <texto>` (el texto sale de `http.StatusText`).
- Las cabeceras `Content-Type: text/plain; charset=utf-8`, `Content-Length: <len(msg)>` y `Connection: close`.
- Una línea vacía, y después `msg`.

Cada línea termina en `\r\n`, no en `\n`. **Por qué:** HTTP/1.1 es texto con una forma fija, y esta es la única respuesta que vas a armar sin ayuda de Go. Cuando la veas pasar la prueba, vas a saber leer cualquier respuesta en crudo.

- [ ] **Paso 4 (Tú): Implementar `readRequest(br *bufio.Reader) (*http.Request, error)`**

Una línea: `http.ReadRequest(br)`. **Por qué existe:** es el único lugar donde se analiza HTTP. Si más adelante escribes tu propio analizador (el enfoque C del diseño), lo cambias solo aquí.

- [ ] **Paso 5 (Tú): Implementar `Serve(ln net.Listener) error`**

Un bucle sobre `ln.Accept()` que lanza `go p.handleConn(conn)` por cada conexión. Si `Accept` falla con `errors.Is(err, net.ErrClosed)`, devuelve `nil`: así es como Ctrl+C apaga el proxy. Con cualquier otro error, lo devuelve. **Por qué una goroutine por conexión:** un navegador abre varias conexiones a la vez. Si las atiendes en fila, una página lenta frena a todas las demás.

- [ ] **Paso 6 (Tú): Implementar `handleConn(client net.Conn)`**

El orden importa:
1. `defer client.Close()`.
2. Crea **un solo** `br := bufio.NewReader(client)` para toda la conexión. **Por qué:** `bufio` lee del socket en bloques y puede traer bytes de la petición siguiente, o del saludo TLS dentro de un túnel. Si creas otro `bufio.Reader` para cada petición, esos bytes se pierden.
3. En bucle:
   - `client.SetReadDeadline(time.Now().Add(p.IdleTimeout))`.
   - `req, err := readRequest(br)`.
   - Si hay error: con `errors.Is(err, io.EOF)` o un `net.Error` con `Timeout()`, termina sin registrar nada (el cliente se fue, o se quedó callado). Con cualquier otro error, llama a `p.Log.Errorf("petición mal formada de %s: %v", client.RemoteAddr(), err)`, responde `writeError(client, 400, "petición mal formada")` y termina.
   - `client.SetReadDeadline(time.Time{})`. **Por qué:** el plazo solo vale mientras esperas una petición nueva. Leer un cuerpo largo o mantener un túnel no cuentan como inactividad.
   - Si `req.Method == http.MethodConnect`: `p.tunnel(client, br, req)` y termina, porque después de un túnel la conexión ya no habla HTTP.
   - Si no: `if !p.forward(client, req) { return }`.

- [ ] **Paso 7: Correr y ver que pasan**

Run: `go test -race -run 'TestWriteError|TestServe|TestMalformed|TestIdle|TestClientHangUp' .`
Expected: `ok`

- [ ] **Paso 8 (Claude): Revisar `conn.go`** contra los pasos 3 a 6 y las restricciones globales. Comenta lo que encuentres. El usuario corrige.

- [ ] **Paso 9: Commit**

```bash
git add conn.go conn_test.go
git commit -m "Aceptar conexiones y atender cada cliente"
```

---

### Tarea 4 (Tú): Cabeceras de salto

**Files:**
- Modify: `forward.go` (reemplazar el stub de `removeHopByHop`)
- Test: `forward_test.go` (lo escribe Claude)

**Interfaces:**
- Produces: `var hopByHopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}` y `func removeHopByHop(h http.Header)`.

- [ ] **Paso 1 (Claude): Escribir las pruebas que fallan** en `forward_test.go`

```go
func TestRemoveHopByHop(t *testing.T) {
	h := http.Header{}
	h.Set("Connection", "X-Secret, Keep-Alive")
	for _, k := range []string{"X-Secret", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Set(k, "x")
	}
	h.Set("X-Normal", "ok")
	removeHopByHop(h)
	// len(h) == 1 && h.Get("X-Normal") == "ok"
}

func TestRemoveHopByHopMultipleConnectionHeaders(t *testing.T) {
	h := http.Header{"Connection": {"A", "B, C"}, "A": {"1"}, "B": {"2"}, "C": {"3"}, "X-Normal": {"ok"}}
	removeHopByHop(h)
	// len(h) == 1 && h.Get("X-Normal") == "ok"
}
```

- [ ] **Paso 2: Correr y ver que fallan**

Run: `go test -run TestRemoveHopByHop .`
Expected: FAIL con `panic: sin implementar: Tarea 4`.

- [ ] **Paso 3 (Tú): Implementar `removeHopByHop`**

Primero recorre **todos** los valores de `h["Connection"]`, sepáralos por coma, quita los espacios (`strings.TrimSpace`) y borra cada cabecera nombrada. Después borra las de `hopByHopHeaders`. **Por qué en ese orden:** `Connection` es la lista de cabeceras que solo valen para este salto. Si la borras primero, pierdes la lista. **Por qué existen:** estas cabeceras describen la conexión entre dos puntos (cliente ↔ proxy, o proxy ↔ destino), no el mensaje, y por eso no se reenvían.

- [ ] **Paso 4: Correr y ver que pasan**

Run: `go test -run TestRemoveHopByHop .`
Expected: `ok`

- [ ] **Paso 5 (Claude): Revisar.** **Paso 6: Commit**

```bash
git add forward.go forward_test.go
git commit -m "Quitar cabeceras de salto"
```

---

### Tarea 5 (Tú): Reenvío HTTP

**Files:**
- Modify: `forward.go` (reemplazar el stub de `forward`)
- Test: `forward_test.go` (Claude agrega estas pruebas)

**Interfaces:**
- Consumes: `removeHopByHop` (Tarea 4); `writeError` (Tarea 3); `p.dial`, `statusForDialError`, `p.Log`, `p.Verbose` (Tarea 2).
- Produces: `func (p *Proxy) forward(client net.Conn, req *http.Request) (keepAlive bool)`, que devuelve si la conexión con el cliente puede seguir.

- [ ] **Paso 1 (Claude): Escribir las pruebas que fallan** en `forward_test.go`

Utilidades de prueba en este archivo:
- `type origin struct { *httptest.Server; mu sync.Mutex; uri string; header http.Header }` y `func newOrigin(t *testing.T) *origin`, más `func (o *origin) last() (uri string, h http.Header)`. El servidor guarda `r.RequestURI` y `r.Header.Clone()`, responde siempre con las cabeceras `X-Upstream: si` y `Keep-Alive: timeout=5`, y según la ruta:
  - `/echo`: copia el cuerpo de la petición.
  - `/chunked`: escribe `"parte1"`, hace `Flush` y escribe `"parte2"`.
  - cualquier otra: `"hola"`.
- `func noLengthOrigin(t *testing.T) string`: un servidor TCP crudo que lee una línea, responde `"HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\ncuerpo sin longitud"` y cierra.

```go
func TestForwardGET(t *testing.T) {
	o := newOrigin(t)
	addr, logs := startProxy(t, nil)
	resp, err := proxyClient(t, addr, nil).Get(o.URL + "/?x=1")
	// err == nil; 200; readBody == "hola"; X-Upstream == "si"; Keep-Alive == ""
	uri, _ := o.last()
	// uri == "/?x=1"   ← forma de origen: el destino nunca ve "http://..."
	waitForLog(t, logs, o.URL+"/?x=1")
	// strings.Contains(logs.String(), " 200  ")
}

func TestForwardPOSTLargeBody(t *testing.T) {
	// cuerpo := bytes.Repeat([]byte("a"), 1<<20); POST a o.URL+"/echo" por el proxy
	// 200; la respuesta es idéntica a cuerpo (bytes.Equal)
}

func TestForwardChunked(t *testing.T) {
	// GET o.URL+"/chunked" por el proxy → readBody == "parte1parte2"
}

func TestForwardKeepAlive(t *testing.T) {
	// rawConn al proxy; dos veces por la MISMA conexión:
	//   escribir "GET "+o.URL+"/ HTTP/1.1\r\nHost: x\r\n\r\n"; ReadResponse → 200, "hola"
}

func TestForwardStripsHopByHopUpstream(t *testing.T) {
	// rawConn; "GET "+o.URL+"/ HTTP/1.1\r\nHost: x\r\nProxy-Connection: keep-alive\r\nConnection: X-Secret\r\n" +
	//   "X-Secret: 1\r\nKeep-Alive: 300\r\nProxy-Authorization: Basic abc\r\nX-Normal: ok\r\n\r\n"
	// ReadResponse → 200 (así sabemos que el destino ya recibió la petición)
	_, h := o.last()
	// h no tiene X-Secret, Proxy-Connection, Keep-Alive ni Proxy-Authorization
	// h.Get("X-Normal") == "ok"
	// h.Get("Connection") == "close"   ← el proxy usa una conexión por petición y se lo avisa al destino
}

func TestForwardUnreachable502(t *testing.T) {
	// GET "http://"+closedAddr(t)+"/" por el proxy → 502; waitForLog(" 502  ")
}

func TestForwardDialTimeout504(t *testing.T) {
	addr, _ := startProxy(t, func(p *Proxy) {
		p.Dial = func(string) (net.Conn, error) { return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded} }
	})
	// GET o.URL por el proxy → 504
}

func TestForwardRejectsNonAbsolute400(t *testing.T) {
	// rawConn; "GET /x HTTP/1.1\r\nHost: a\r\n\r\n" → 400; expectClosed
}

func TestForwardRejectsNonHTTPScheme400(t *testing.T) {
	// rawConn; "GET https://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n" → 400 (sin tocar la red)
}

func TestForwardVerboseLogsHeaders(t *testing.T) {
	// Verbose = true; rawConn con "Proxy-Connection: keep-alive"
	// waitForLog de: "--- petición del cliente ---", "--- petición al destino ---",
	//   "--- respuesta del destino ---", "--- respuesta al cliente ---", "Proxy-Connection: keep-alive"
}

// Review Focus
func TestForwardBodyWithoutLength(t *testing.T) {
	// rawConn; GET "http://"+noLengthOrigin(t)+"/" → 200, readBody == "cuerpo sin longitud", resp.Close; expectClosed
}

func TestForwardSkips100Continue(t *testing.T) {
	// rawConn; en una sola escritura:
	//   "POST "+o.URL+"/echo HTTP/1.1\r\nHost: x\r\nExpect: 100-continue\r\nContent-Length: 5\r\n\r\nhello"
	// la PRIMERA respuesta leída es 200 (no 100) y readBody == "hello"
}

func TestForwardHEADKeepsConnection(t *testing.T) {
	// rawConn; "HEAD "+o.URL+"/ ..." → ReadResponse(br, &http.Request{Method: "HEAD"}) → 200, ContentLength 4, cuerpo vacío
	// luego por la misma conexión "GET "+o.URL+"/ ..." → 200, "hola"
}

func TestForwardClientConnectionClose(t *testing.T) {
	// rawConn; GET con "Connection: close" → 200, "hola", resp.Close; expectClosed (mucho antes de 60 s)
}
```

- [ ] **Paso 2: Correr y ver que fallan**

Run: `go test -run TestForward .`
Expected: FAIL con `panic: sin implementar: Tarea 5`.

- [ ] **Paso 3 (Tú): Implementar `forward`**

En orden:
1. `start := time.Now()`, `target := req.URL.String()` (para el log).
2. **Validar.** Si `req.URL.Host == ""` o `req.URL.Scheme != "http"`: `writeError(client, 400, "se esperaba una URL absoluta http://")`, `p.Log.HTTPError(req.Method, target, 400, err)` con ese mismo mensaje como error, y `return false`. **Por qué:** a un proxy siempre se le habla con la URL completa. Un `GET /x` significa que el cliente cree que está hablando con un servidor.
3. **Destino:** `net.JoinHostPort(req.URL.Hostname(), puerto)`, con `"80"` si `req.URL.Port()` viene vacío. `JoinHostPort` también maneja IPv6.
4. Si `p.Verbose`: `p.Log.Headers("petición del cliente", req.Header)`.
5. **Preparar la petición.** Guarda `clientClose := req.Close` **antes** de tocar nada. Después llama a `removeHopByHop(req.Header)` y pon `req.Close = true`. **Por qué:** abrimos una conexión al destino por cada petición, y `Connection: close` le avisa que la cierre al terminar. Lo que pidió el cliente se decide aparte, con `clientClose`. Si `p.Verbose`: `Headers("petición al destino", req.Header)`.
6. **Conectar:** `up, err := p.dial(addr)`. Si falla: `status := statusForDialError(err)`, `writeError(client, status, err.Error())`, `HTTPError`, `return false`. Si funciona: `defer up.Close()`.
7. **Enviar:** `req.Write(up)`. Error → `502`. Deja un comentario: `Write` escribe la petición en forma de origen (`GET /ruta HTTP/1.1`) y `WriteProxy` la escribiría en forma absoluta. Esa es justo la diferencia entre hablarle a un servidor y hablarle a un proxy.
8. **Leer la respuesta:** `upBr := bufio.NewReader(up)` y `resp, err := http.ReadResponse(upBr, req)`. Pasa `req` para que Go sepa que una respuesta a `HEAD` no trae cuerpo. **Salta las respuestas 1xx** que no sean 101: vuelve a llamar `ReadResponse` mientras `100 <= resp.StatusCode < 200 && resp.StatusCode != 101`. **Por qué:** con `Expect: 100-continue`, el destino manda primero un `100 Continue` y después la respuesta de verdad. Error → `502`. Si funciona: `defer resp.Body.Close()`.
9. **Decidir el keep-alive.** Si `p.Verbose`: `Headers("respuesta del destino", resp.Header)`. Después:
   - `removeHopByHop(resp.Header)`.
   - `keepAlive := !clientClose && (resp.ContentLength >= 0 || chunked)`, donde `chunked` es `len(resp.TransferEncoding) > 0 && resp.TransferEncoding[0] == "chunked"`.
   - `resp.Close = !keepAlive`.

   **Por qué:** el `Connection: close` del destino solo describe la conexión proxy ↔ destino, así que no le importa al cliente. Con el cliente solo cerramos si él lo pidió, o si la respuesta no tiene longitud: en ese caso el fin del cuerpo se marca cerrando la conexión. Si `p.Verbose`: `Headers("respuesta al cliente", resp.Header)`.
10. **Devolver:** `resp.Write(client)`. Si falla: `p.Log.Errorf(...)` y `return false`.
11. `p.Log.HTTP(req.Method, target, resp.StatusCode, time.Since(start))` y `return keepAlive`.

Detalle opcional: si el cliente no mandó `User-Agent`, `req.Write` agrega `User-Agent: Go-http-client/1.1`. Para no inventarlo, pon `req.Header["User-Agent"] = []string{""}` cuando falte.

- [ ] **Paso 4: Correr y ver que pasan**

Run: `go test -race -run 'TestForward|TestRemoveHopByHop' .`
Expected: `ok`

- [ ] **Paso 5 (Claude): Revisar.** **Paso 6: Commit**

```bash
git add forward.go forward_test.go
git commit -m "Reenvío HTTP con keep-alive"
```

---

### Tarea 6 (Tú): Túnel CONNECT

**Files:**
- Modify: `tunnel.go` (reemplazar el stub)
- Test: `tunnel_test.go` (lo escribe Claude)

**Interfaces:**
- Consumes: `writeError` (Tarea 3); `p.dial`, `statusForDialError`, `p.Log` (Tarea 2).
- Produces: `func (p *Proxy) tunnel(client net.Conn, br *bufio.Reader, req *http.Request)`. `handleConn` cierra `client` cuando termina.

- [ ] **Paso 1 (Claude): Escribir las pruebas que fallan** en `tunnel_test.go`

Utilidades en este archivo:
- `func echoServer(t *testing.T) string`: por cada conexión hace `io.Copy(conn, conn)` y cierra.
- `func connect(t *testing.T, proxyAddr, target, extra string) (net.Conn, *bufio.Reader)`: hace `rawConn`, escribe `"CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n"+extra` en **una sola escritura**, lee dos líneas con `br.ReadString('\n')` y exige `"HTTP/1.1 200 Connection Established\r\n"` y `"\r\n"`.

```go
func TestTunnelHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hola por https") }))
	t.Cleanup(srv.Close)
	addr, logs := startProxy(t, nil)
	client := proxyClient(t, addr, srv.Client().Transport.(*http.Transport))
	resp, err := client.Get(srv.URL)
	// err == nil; 200; readBody == "hola por https"
	waitForLog(t, logs, "túnel abierto")
	client.CloseIdleConnections()
	waitForLog(t, logs, "cerrado")
}

func TestTunnelKeepsBufferedBytes(t *testing.T) {
	// conn, br := connect(t, addr, echoServer(t), "PING")   ← "PING" viaja pegado al CONNECT
	// io.ReadFull(br, 4 bytes) == "PING"   (si el proxy copia desde la conexión cruda, se pierde)
}

func TestTunnelEchoBothWays(t *testing.T) {
	// conn, br := connect(t, addr, echoServer(t), "")
	// escribir "hola" → leer "hola"
	// conn.(*net.TCPConn).CloseWrite() → expectClosed(t, br)   ← el cierre se propaga de ida y vuelta
	// waitForLog(t, logs, "4B↑ 4B↓")
}

func TestTunnelMissingPort400(t *testing.T) {
	// rawConn; "CONNECT nohost HTTP/1.1\r\nHost: nohost\r\n\r\n" → 400
}

func TestTunnelUnreachable502(t *testing.T) {
	// rawConn; CONNECT a closedAddr(t) → 502; waitForLog(" 502  ")
}

// Review Focus
func TestTunnelServerClosesFirst(t *testing.T) {
	// servidor TCP que al aceptar escribe "adios" y cierra
	// conn, br := connect(t, addr, ese servidor, "")
	// leer "adios"; expectClosed(t, br); conn.Close()
	// waitForLog(t, logs, "cerrado  0B↑ 5B↓")
}
```

- [ ] **Paso 2: Correr y ver que fallan**

Run: `go test -run TestTunnel .`
Expected: FAIL con `panic: sin implementar: Tarea 6`.

- [ ] **Paso 3 (Tú): Implementar `tunnel`**

1. `target := req.Host`: en un `CONNECT` llega como `host:puerto`. Si `net.SplitHostPort(target)` falla o el puerto viene vacío: `writeError(client, 400, ...)`, `p.Log.HTTPError("CONNECT", target, 400, err)` y `return`.
2. `up, err := p.dial(target)`. Si falla: `statusForDialError`, `writeError`, `HTTPError` y `return`. Si funciona: `defer up.Close()`.
3. Escribe `"HTTP/1.1 200 Connection Established\r\n\r\n"` al cliente y llama a `p.Log.TunnelOpen(target)`. **Desde aquí el proxy ya no entiende nada:** lo que pasa es TLS cifrado, y solo copia bytes.
4. `start := time.Now()`. Lanza dos goroutines y espéralas con un `sync.WaitGroup`:
   - **subida:** `upBytes, _ = io.Copy(up, br)` y después `closeWrite(up)`. **Copia desde `br`, no desde `client`:** el `bufio.Reader` puede tener guardado el comienzo del saludo TLS que llegó junto con el `CONNECT`.
   - **bajada:** `downBytes, _ = io.Copy(client, up)` y después `closeWrite(client)`.
5. `p.Log.TunnelClosed(target, upBytes, downBytes, time.Since(start))`.

Escribe también `func closeWrite(c net.Conn)`: si `c` implementa `interface{ CloseWrite() error }`, lo llama. **Por qué cerrar solo la mitad de escritura:** cuando un lado termina de mandar, el otro puede seguir respondiendo. Si cierras todo de golpe, cortas respuestas que todavía vienen en camino. Con `CloseWrite`, el otro extremo recibe EOF, pero la conexión sigue viva en la otra dirección.

- [ ] **Paso 4: Correr y ver que pasan**

Run: `go test -race -run TestTunnel .`
Expected: `ok`

- [ ] **Paso 5 (Claude): Revisar.** **Paso 6: Commit**

```bash
git add tunnel.go tunnel_test.go
git commit -m "Túnel CONNECT"
```

---

### Tarea 7 (Claude y tú): Verificación de punta a punta

**Files:** ninguno nuevo (solo correcciones si algo falla).

- [ ] **Paso 1: Pruebas completas y chequeos estáticos**

Run: `go test -race ./... && go vet ./... && gofmt -l .`
Expected: `ok`, sin salida de `vet` ni de `gofmt`.

- [ ] **Paso 2: Con curl**

Terminal 1: `go run .`. Debería imprimir `goproxy escuchando en 127.0.0.1:8080`.
Terminal 2:
- `curl -x http://127.0.0.1:8080 http://example.com/` → el HTML de example.com. En la terminal 1 aparece la línea `GET http://example.com/ 200`.
- `curl -x http://127.0.0.1:8080 https://example.com/` → el HTML. En la terminal 1 aparecen `CONNECT example.com:443 túnel abierto` y luego `cerrado`, con los bytes.
- `curl -x http://127.0.0.1:8080 http://nohay.invalid/` → `502`.

- [ ] **Paso 3: Modo detallado**

`go run . -v` y repetir el primer `curl`. Aparecen las cuatro secciones de cabeceras, y `Proxy-Connection` solo en la del cliente.

- [ ] **Paso 4: Navegador**

Configura el proxy HTTP y HTTPS como `127.0.0.1:8080`: en Windows, Configuración → Red e Internet → Proxy; en Firefox, desde sus propios ajustes de red. Navega un rato y mira la terminal. Si el navegador de Windows no llega al `localhost` de WSL2, alcanza con curl (ver Restricciones en el diseño). Al final, quita el proxy del sistema.

- [ ] **Paso 5: Apagado**

Ctrl+C en la terminal 1: el proceso termina enseguida.

- [ ] **Paso 6 (Claude): Revisión final** de toda la rama contra el diseño. Después, commit de cualquier ajuste.
