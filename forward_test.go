package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRemoveHopByHop(t *testing.T) {
	h := http.Header{}
	// Connection nombra X-Secret: aunque no esté en la lista fija, también es
	// de salto para este mensaje.
	h.Set("Connection", "X-Secret, Keep-Alive")
	for _, k := range []string{"X-Secret", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Set(k, "x")
	}
	h.Set("X-Normal", "ok")

	removeHopByHop(h)

	if len(h) != 1 || h.Get("X-Normal") != "ok" {
		t.Errorf("solo debería quedar X-Normal; quedó %v", h)
	}
}

func TestRemoveHopByHopMultipleConnectionHeaders(t *testing.T) {
	// Connection puede venir repetida, y cada valor puede traer varios
	// nombres separados por coma.
	h := http.Header{
		"Connection": {"A", "B, C"},
		"A":          {"1"},
		"B":          {"2"},
		"C":          {"3"},
		"X-Normal":   {"ok"},
	}

	removeHopByHop(h)

	if len(h) != 1 || h.Get("X-Normal") != "ok" {
		t.Errorf("solo debería quedar X-Normal; quedó %v", h)
	}
}

// --- Utilidades para probar el reenvío ---

// origin es un servidor de destino que recuerda la última petición que le
// llegó, para comprobar qué le mandó el proxy.
type origin struct {
	*httptest.Server
	mu     sync.Mutex
	uri    string
	header http.Header
}

func newOrigin(t *testing.T) *origin {
	t.Helper()
	o := &origin{}
	o.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		o.uri = r.RequestURI
		o.header = r.Header.Clone()
		o.mu.Unlock()

		w.Header().Set("X-Upstream", "si")
		// Keep-Alive es de salto: el proxy no debería pasársela al cliente.
		w.Header().Set("Keep-Alive", "timeout=5")
		switch r.URL.Path {
		case "/echo":
			// Se lee todo antes de responder: un servidor HTTP/1 de Go no
			// garantiza poder seguir leyendo el cuerpo una vez que empezó a
			// escribir la respuesta.
			body, _ := io.ReadAll(r.Body)
			w.Write(body)
		case "/chunked":
			// El Flush manda la primera parte antes de saber el largo
			// total, así que Go responde con Transfer-Encoding: chunked.
			io.WriteString(w, "parte1")
			w.(http.Flusher).Flush()
			io.WriteString(w, "parte2")
		default:
			io.WriteString(w, "hola")
		}
	}))
	t.Cleanup(o.Close)
	return o
}

// last devuelve la ruta y las cabeceras de la última petición recibida.
func (o *origin) last() (uri string, h http.Header) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.uri, o.header
}

// noLengthOrigin es un destino que responde sin Content-Length ni chunked: el
// fin del cuerpo lo marca cerrando la conexión, como en HTTP/1.0.
func noLengthOrigin(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				// Leer las cabeceras completas antes de cerrar: cerrar con
				// datos sin leer hace que el sistema mande RST en vez de FIN,
				// y el proxy podría perder la respuesta.
				br := bufio.NewReader(conn)
				for {
					line, err := br.ReadString('\n')
					if err != nil || line == "\r\n" {
						break
					}
				}
				io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\ncuerpo sin longitud")
			}()
		}
	}()
	return ln.Addr().String()
}

// readResponse lee una respuesta del proxy. method le dice a Go si la
// petición fue HEAD, porque esa respuesta no trae cuerpo.
func readResponse(t *testing.T, br *bufio.Reader, method string) *http.Response {
	t.Helper()
	resp, err := http.ReadResponse(br, &http.Request{Method: method})
	if err != nil {
		t.Fatalf("leyendo la respuesta del proxy: %v", err)
	}
	return resp
}

// --- Reenvío ---

func TestForwardGET(t *testing.T) {
	o := newOrigin(t)
	addr, logs := startProxy(t, nil)

	resp, err := proxyClient(t, addr, nil).Get(o.URL + "/?x=1")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("StatusCode = %d, quiero 200", resp.StatusCode)
	}
	if body := readBody(t, resp.Body); body != "hola" {
		t.Errorf("cuerpo = %q, quiero %q", body, "hola")
	}
	if got := resp.Header.Get("X-Upstream"); got != "si" {
		t.Errorf("X-Upstream = %q: el proxy perdió una cabecera normal del destino", got)
	}
	if got := resp.Header.Get("Keep-Alive"); got != "" {
		t.Errorf("Keep-Alive = %q: es de salto y no debería llegar al cliente", got)
	}

	// Forma de origen: el destino ve solo la ruta, nunca "http://...".
	if uri, _ := o.last(); uri != "/?x=1" {
		t.Errorf("el destino recibió la ruta %q, quiero %q", uri, "/?x=1")
	}

	waitForLog(t, logs, o.URL+"/?x=1")
	if !strings.Contains(logs.String(), " 200  ") {
		t.Errorf("el log no muestra el código 200:\n%s", logs.String())
	}
}

func TestForwardPOSTLargeBody(t *testing.T) {
	o := newOrigin(t)
	addr, _ := startProxy(t, nil)
	body := bytes.Repeat([]byte("a"), 1<<20)

	resp, err := proxyClient(t, addr, nil).Post(o.URL+"/echo", "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("StatusCode = %d, quiero 200", resp.StatusCode)
	}
	if got := readBody(t, resp.Body); got != string(body) {
		t.Errorf("el eco tiene %d bytes, quiero %d iguales a los enviados", len(got), len(body))
	}
}

func TestForwardChunked(t *testing.T) {
	o := newOrigin(t)
	addr, _ := startProxy(t, nil)
	conn, br := rawConn(t, addr)

	io.WriteString(conn, "GET "+o.URL+"/chunked HTTP/1.1\r\nHost: x\r\n\r\n")
	resp := readResponse(t, br, "GET")
	if len(resp.TransferEncoding) == 0 || resp.TransferEncoding[0] != "chunked" {
		t.Errorf("TransferEncoding = %v, quiero [chunked]", resp.TransferEncoding)
	}
	if body := readBody(t, resp.Body); body != "parte1parte2" {
		t.Errorf("cuerpo = %q, quiero %q", body, "parte1parte2")
	}

	// Un cuerpo chunked marca su propio final, así que la conexión puede
	// seguir.
	io.WriteString(conn, "GET "+o.URL+"/ HTTP/1.1\r\nHost: x\r\n\r\n")
	resp = readResponse(t, br, "GET")
	if body := readBody(t, resp.Body); body != "hola" {
		t.Errorf("segunda petición: cuerpo = %q, quiero %q", body, "hola")
	}
}

func TestForwardKeepAlive(t *testing.T) {
	o := newOrigin(t)
	addr, _ := startProxy(t, nil)
	conn, br := rawConn(t, addr)

	for i := 1; i <= 2; i++ {
		io.WriteString(conn, "GET "+o.URL+"/ HTTP/1.1\r\nHost: x\r\n\r\n")
		resp := readResponse(t, br, "GET")
		if resp.StatusCode != 200 {
			t.Errorf("petición %d: StatusCode = %d, quiero 200", i, resp.StatusCode)
		}
		if body := readBody(t, resp.Body); body != "hola" {
			t.Errorf("petición %d: cuerpo = %q, quiero %q", i, body, "hola")
		}
		if resp.Close {
			t.Errorf("petición %d: el proxy anunció Connection: close, pero el cliente no lo pidió", i)
		}
	}
}

func TestForwardStripsHopByHopUpstream(t *testing.T) {
	o := newOrigin(t)
	addr, _ := startProxy(t, nil)
	conn, br := rawConn(t, addr)

	io.WriteString(conn, "GET "+o.URL+"/ HTTP/1.1\r\nHost: x\r\n"+
		"Proxy-Connection: keep-alive\r\n"+
		"Connection: X-Secret\r\n"+
		"X-Secret: 1\r\n"+
		"Keep-Alive: 300\r\n"+
		"Proxy-Authorization: Basic abc\r\n"+
		"X-Normal: ok\r\n\r\n")
	// Leer la respuesta garantiza que el destino ya recibió la petición.
	resp := readResponse(t, br, "GET")
	readBody(t, resp.Body)

	_, h := o.last()
	for _, k := range []string{"X-Secret", "Proxy-Connection", "Keep-Alive", "Proxy-Authorization"} {
		if v := h.Get(k); v != "" {
			t.Errorf("el destino recibió %s: %q; es de salto", k, v)
		}
	}
	if got := h.Get("X-Normal"); got != "ok" {
		t.Errorf("X-Normal = %q: el proxy perdió una cabecera normal", got)
	}
	// El proxy usa una conexión nueva por petición y se lo avisa al destino.
	if got := h.Get("Connection"); got != "close" {
		t.Errorf("Connection hacia el destino = %q, quiero %q", got, "close")
	}
}

func TestForwardUnreachable502(t *testing.T) {
	addr, logs := startProxy(t, nil)

	resp, err := proxyClient(t, addr, nil).Get("http://" + closedAddr(t) + "/")
	if err != nil {
		t.Fatal(err)
	}
	readBody(t, resp.Body)
	if resp.StatusCode != 502 {
		t.Errorf("StatusCode = %d, quiero 502", resp.StatusCode)
	}
	waitForLog(t, logs, " 502  ")
}

func TestForwardDialTimeout504(t *testing.T) {
	o := newOrigin(t)
	addr, logs := startProxy(t, func(p *Proxy) {
		p.Dial = func(string) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
		}
	})

	resp, err := proxyClient(t, addr, nil).Get(o.URL)
	if err != nil {
		t.Fatal(err)
	}
	readBody(t, resp.Body)
	if resp.StatusCode != 504 {
		t.Errorf("StatusCode = %d, quiero 504", resp.StatusCode)
	}
	waitForLog(t, logs, " 504  ")
}

func TestForwardRejectsNonAbsolute400(t *testing.T) {
	addr, _ := startProxy(t, nil)
	conn, br := rawConn(t, addr)

	// Un cliente que manda "GET /x" cree que habla con un servidor, no con un
	// proxy.
	io.WriteString(conn, "GET /x HTTP/1.1\r\nHost: a\r\n\r\n")
	resp := readResponse(t, br, "GET")
	readBody(t, resp.Body)
	if resp.StatusCode != 400 {
		t.Errorf("StatusCode = %d, quiero 400", resp.StatusCode)
	}
	expectClosed(t, br)
}

func TestForwardRejectsNonHTTPScheme400(t *testing.T) {
	// Si el proxy intentara conectarse, este Dial fallaría y la respuesta
	// sería 502 en vez de 400.
	addr, _ := startProxy(t, func(p *Proxy) {
		p.Dial = func(string) (net.Conn, error) { return nil, errors.New("no debería conectarse a ningún lado") }
	})
	conn, br := rawConn(t, addr)

	// HTTPS por un proxy va con CONNECT; una URL https:// en un GET no se
	// puede atender sin cifrar nosotros mismos.
	io.WriteString(conn, "GET https://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n")
	resp := readResponse(t, br, "GET")
	readBody(t, resp.Body)
	if resp.StatusCode != 400 {
		t.Errorf("StatusCode = %d, quiero 400", resp.StatusCode)
	}
	expectClosed(t, br)
}

func TestForwardVerboseLogsHeaders(t *testing.T) {
	o := newOrigin(t)
	addr, logs := startProxy(t, func(p *Proxy) { p.Verbose = true })
	conn, br := rawConn(t, addr)

	io.WriteString(conn, "GET "+o.URL+"/ HTTP/1.1\r\nHost: x\r\nProxy-Connection: keep-alive\r\n\r\n")
	resp := readResponse(t, br, "GET")
	readBody(t, resp.Body)

	for _, s := range []string{
		"--- petición del cliente ---",
		"--- petición al destino ---",
		"--- respuesta del destino ---",
		"--- respuesta al cliente ---",
		" 200  ",
	} {
		waitForLog(t, logs, s)
	}
	// Aparece en lo que mandó el cliente y en ningún volcado posterior.
	if n := strings.Count(logs.String(), "Proxy-Connection: keep-alive"); n != 1 {
		t.Errorf("Proxy-Connection aparece %d veces, quiero 1 (solo en la petición del cliente); log:\n%s", n, logs.String())
	}
}

// --- Review Focus ---

func TestForwardBodyWithoutLength(t *testing.T) {
	addr, _ := startProxy(t, nil)
	conn, br := rawConn(t, addr)

	io.WriteString(conn, "GET http://"+noLengthOrigin(t)+"/ HTTP/1.1\r\nHost: x\r\n\r\n")
	resp := readResponse(t, br, "GET")
	if resp.StatusCode != 200 {
		t.Errorf("StatusCode = %d, quiero 200", resp.StatusCode)
	}
	// El cuerpo termina cuando el proxy cierra. Tiene que cerrar porque sabe
	// que debe hacerlo, no porque se cumplió el plazo de inactividad (5 s en
	// las pruebas): si tarda más de 1 s, readBody falla.
	conn.SetDeadline(time.Now().Add(time.Second))
	if body := readBody(t, resp.Body); body != "cuerpo sin longitud" {
		t.Errorf("cuerpo = %q, quiero %q", body, "cuerpo sin longitud")
	}
	// Sin longitud, el cliente solo sabe que el cuerpo terminó porque se
	// cierra la conexión: el proxy tiene que avisarlo y cerrar.
	if !resp.Close {
		t.Errorf("falta Connection: close en una respuesta sin longitud")
	}
	expectClosed(t, br)
}

func TestForwardSkips100Continue(t *testing.T) {
	o := newOrigin(t)
	addr, _ := startProxy(t, nil)
	conn, br := rawConn(t, addr)

	// curl manda Expect: 100-continue con cuerpos de más de 1 KB.
	io.WriteString(conn, "POST "+o.URL+"/echo HTTP/1.1\r\nHost: x\r\nExpect: 100-continue\r\nContent-Length: 5\r\n\r\nhello")
	resp := readResponse(t, br, "POST")
	if resp.StatusCode != 200 {
		t.Fatalf("la primera respuesta es %d, quiero 200 (el 100 Continue del destino no debería llegar al cliente)", resp.StatusCode)
	}
	if body := readBody(t, resp.Body); body != "hello" {
		t.Errorf("cuerpo = %q, quiero %q", body, "hello")
	}
}

func TestForwardHEADKeepsConnection(t *testing.T) {
	o := newOrigin(t)
	addr, _ := startProxy(t, nil)
	conn, br := rawConn(t, addr)

	io.WriteString(conn, "HEAD "+o.URL+"/ HTTP/1.1\r\nHost: x\r\n\r\n")
	resp := readResponse(t, br, "HEAD")
	if resp.StatusCode != 200 {
		t.Errorf("HEAD: StatusCode = %d, quiero 200", resp.StatusCode)
	}
	if resp.ContentLength != 4 {
		t.Errorf("HEAD: ContentLength = %d, quiero 4 (el largo que tendría el GET)", resp.ContentLength)
	}
	if body := readBody(t, resp.Body); body != "" {
		t.Errorf("HEAD: cuerpo = %q, quiero vacío", body)
	}

	// Si el proxy esperara los 4 bytes del cuerpo de HEAD, se quedaría
	// colgado y esta segunda petición nunca tendría respuesta.
	io.WriteString(conn, "GET "+o.URL+"/ HTTP/1.1\r\nHost: x\r\n\r\n")
	resp = readResponse(t, br, "GET")
	if body := readBody(t, resp.Body); body != "hola" {
		t.Errorf("GET después de HEAD: cuerpo = %q, quiero %q", body, "hola")
	}
}

func TestForwardClientConnectionClose(t *testing.T) {
	o := newOrigin(t)
	addr, _ := startProxy(t, nil)
	conn, br := rawConn(t, addr)

	io.WriteString(conn, "GET "+o.URL+"/ HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	resp := readResponse(t, br, "GET")
	if body := readBody(t, resp.Body); body != "hola" {
		t.Errorf("cuerpo = %q, quiero %q", body, "hola")
	}
	if !resp.Close {
		t.Errorf("el cliente pidió Connection: close y la respuesta no lo confirma")
	}
	// Tiene que cerrar enseguida, no cuando se cumpla el plazo de
	// inactividad (5 s en las pruebas, 60 s en la realidad).
	conn.SetDeadline(time.Now().Add(time.Second))
	expectClosed(t, br)
}

// El plazo de inactividad solo cubre la espera de una petición nueva: un
// cuerpo que llega lento no es un cliente inactivo.
func TestForwardSlowBodyOutlivesIdleTimeout(t *testing.T) {
	o := newOrigin(t)
	addr, _ := startProxy(t, func(p *Proxy) { p.IdleTimeout = 200 * time.Millisecond })
	conn, br := rawConn(t, addr)

	io.WriteString(conn, "POST "+o.URL+"/echo HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n\r\nhello")
	time.Sleep(500 * time.Millisecond)
	io.WriteString(conn, "world")

	resp := readResponse(t, br, "POST")
	if resp.StatusCode != 200 {
		t.Errorf("StatusCode = %d, quiero 200", resp.StatusCode)
	}
	if body := readBody(t, resp.Body); body != "helloworld" {
		t.Errorf("cuerpo = %q, quiero %q", body, "helloworld")
	}
}
