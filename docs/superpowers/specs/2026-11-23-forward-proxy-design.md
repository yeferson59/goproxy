# Proxy de salida en Go — Diseño

Fecha: 2026-11-23
Estado: borrador, pendiente de aprobación

## Intención

Construir desde cero, en Go, un **proxy de salida** (forward proxy): los
programas del usuario (navegador, `curl`) le mandan su tráfico y el proxy se
conecta a internet por ellos.

El objetivo es **aprender cómo funciona un proxy por dentro**, no tener una
herramienta de producción. Cada decisión se juzga por cuánto deja ver de la
mecánica.

## Cómo se va a construir

El usuario conoce Go en general, más o menos. Por eso las explicaciones se
centran en lo propio del proxy y de redes (`net`, `bufio`, plazos de lectura,
`CloseWrite`, formato HTTP en el cable) y no repasan lo básico del lenguaje.

Modo **mixto** (propuesto por Claude, el usuario no eligió todavía):

- **Claude escribe** lo que no enseña nada del proxy: banderas, apagado,
  formato del log, y las pruebas de cada paso, antes de que exista el código
  que las hace pasar.
- **El usuario escribe** las partes clave: aceptar conexiones y el bucle de
  `handleConn`, el reenvío HTTP y el túnel `CONNECT`. Cada paso trae qué hacer,
  por qué y qué prueba tiene que pasar; Claude revisa cuando el usuario termina.

El plan de implementación tiene que estar escrito para este modo: pasos chicos,
cada uno con su prueba, su explicación y una marca clara de quién escribe qué.

## Objetivos

1. Aceptar conexiones TCP en una dirección local y atender cada cliente en su
   propia goroutine.
2. Reenviar peticiones HTTP en texto claro (`GET http://host/ruta HTTP/1.1`) al
   servidor de destino y devolver la respuesta.
3. Atender `CONNECT host:puerto` abriendo un túnel TCP y copiando bytes en las
   dos direcciones (así pasa HTTPS por un proxy).
4. Mantener abierta la conexión con el cliente entre peticiones HTTP
   (keep-alive), como hace un navegador real.
5. Mostrar en la terminal cada petición y cada túnel, para ver lo que pasa
   mientras se navega.
6. Funcionar con `curl -x` y con un navegador real.

## No objetivos (por ahora)

- **Leer el contenido de HTTPS** (MITM con autoridad de certificados propia).
  Es la segunda etapa, cuando esto funcione.
- Autenticación del proxy (`Proxy-Authorization`).
- SOCKS5.
- Caché, HTTP/2, HTTP/3, filtros o bloqueo de sitios.
- Rendimiento o robustez de producción; archivo de configuración.
- Reutilizar conexiones hacia los servidores de destino.

## Anti-objetivos (sería un fracaso aunque funcione)

- **Usar piezas que escondan la mecánica**: `http.Server`, `http.Transport`,
  `http.Client`, `httputil.ReverseProxy` o librerías de proxy de terceros
  dentro del proxy. Si Go hace por debajo una pieza importante, no se aprende.
- Código compacto o "ingenioso" que cueste leer. Se prefiere claro, con
  comentarios donde la razón no sea obvia.
- Quedar como proxy abierto a la red: por defecto escucha solo en `127.0.0.1`.

## Restricciones

- Go 1.27.4, módulo `github.com/yeferson59/goproxy` (el `go.mod` ya existe).
- Solo librería estándar, sin dependencias externas.
- De `net/http`, el proxy solo usa `http.ReadRequest`, `http.ReadResponse`, los
  tipos `Request`, `Response` y `Header`, y sus métodos `Write`.
- Entorno: Linux sobre WSL2. Las pruebas manuales se hacen con
  `curl -x http://127.0.0.1:8080 ...`. Un navegador en Windows normalmente
  llega al `localhost` de WSL2; si no, alcanza con curl.

## Enfoques considerados

- **A. Sobre `net/http`** (`http.Server` + `http.Transport` + `Hijack` para
  `CONNECT`). Unas 150 líneas y funciona rápido, pero Go esconde cómo se
  aceptan las conexiones, cómo viaja la petición, qué cabeceras se quitan y
  cuándo se cierra una conexión. Descartado: va contra el objetivo de aprender.
- **B. Sockets TCP con `net`; Go solo analiza el texto HTTP.** Unas 300 a 400
  líneas. Todo lo que hace un proxy se escribe a mano; solo se delega leer el
  texto de la petición y de la respuesta. **Elegido por el usuario.**
- **C. Todo a mano, incluido el analizador HTTP.** Es lo que más enseña de HTTP,
  pero el analizador (cabeceras, `Content-Length`, `chunked`) se come el
  proyecto y deja el proxy en segundo plano. Queda como ejercicio futuro posible:
  B aísla la lectura de la petición en una sola función para poder cambiarla
  después por un analizador propio.

## Diseño

### Flujo general

```
cliente ──TCP──▶ proxy (127.0.0.1:8080)
                   │ lee una petición (http.ReadRequest)
                   ├─ CONNECT ──▶ túnel: conecta a host:puerto,
                   │              responde "200 Connection Established",
                   │              copia bytes en ambas direcciones
                   └─ otro ─────▶ reenvío: conecta al destino, envía la
                                  petición reescrita, lee la respuesta y
                                  la devuelve; si la conexión sigue viva,
                                  vuelve a leer la siguiente petición
```

### Aceptar conexiones

- `net.Listen("tcp", addr)`; por cada `Accept`, `go handleConn(conn)`.
- `addr` sale de la bandera `-addr`, por defecto `127.0.0.1:8080`.
- Un error en una conexión nunca tumba el proceso: se registra y se cierra
  solo esa conexión.

### Reenvío HTTP

Por cada petición leída del cliente:

1. Validar que la URL sea absoluta y con esquema `http`
   (`http://host/...`). Si no → `400 Bad Request`.
2. Calcular el destino `host:puerto` (puerto 80 si no viene).
3. Quitar las cabeceras de salto (hop-by-hop): `Connection`,
   `Proxy-Connection`, `Keep-Alive`, `Proxy-Authenticate`,
   `Proxy-Authorization`, `TE`, `Trailer`, `Transfer-Encoding`, `Upgrade`, y
   cualquier cabecera nombrada dentro de `Connection`. (Go ya guarda la
   codificación y la longitud del cuerpo en campos del `Request`, y `Write` las
   vuelve a escribir bien.)
4. Enviar la petición en forma de origen (`GET /ruta?query HTTP/1.1` con su
   `Host`). `Request.Write` escribe esa forma; `WriteProxy` escribiría la forma
   absoluta. Explicar esa diferencia en un comentario, porque es justo lo que
   distingue hablar con un proxy de hablar con un servidor.
5. Conectarse al destino (`net.DialTimeout`), escribir la petición y leer la
   respuesta con `http.ReadResponse`.
6. Quitar las cabeceras de salto de la respuesta y escribirla al cliente con
   `Response.Write`.
7. Registrar la línea de log.
8. Cerrar la conexión con el destino. Con el cliente: si la petición o la
   respuesta piden cerrar (`req.Close`, `resp.Close`, que `Response.Write`
   activa cuando el cuerpo no tiene longitud definida), cerrar; si no, volver
   al paso 1 con la siguiente petición.

Una conexión nueva hacia el destino por cada petición: es lo más simple de
seguir.

### Túnel CONNECT

1. El destino viene como `host:puerto` en la petición. Sin puerto → `400`.
2. Conectarse al destino con `net.DialTimeout`. Si falla → `502` o `504` (ver
   Errores) y cerrar.
3. Responder `HTTP/1.1 200 Connection Established\r\n\r\n`.
4. Copiar en las dos direcciones con dos goroutines e `io.Copy`.
   **Ojo:** el `bufio.Reader` del cliente puede tener bytes ya leídos (por
   ejemplo, el comienzo del saludo TLS). Hay que copiar desde ese
   `bufio.Reader`, no desde la conexión cruda, o esos bytes se pierden.
5. Cuando un lado termina, cerrar la mitad de escritura del otro (`CloseWrite`).
   Cuando terminan los dos, cerrar ambas conexiones y registrar bytes enviados,
   bytes recibidos y duración.

### Errores y tiempos

| Situación | Respuesta al cliente |
|-----------|----------------------|
| Petición mal formada o URL no absoluta | `400 Bad Request` |
| Destino inalcanzable (DNS, conexión rechazada) | `502 Bad Gateway` |
| Se agota el tiempo al conectar al destino | `504 Gateway Timeout` |

- Conectar al destino: 10 s como máximo.
- Cliente inactivo entre peticiones HTTP: cerrar a los 60 s (`SetReadDeadline`).
- Túneles: sin límite de inactividad (una pestaña puede mantener un túnel
  abierto mucho tiempo).

### Registro

Una línea por evento en la salida estándar:

```
14:02:11 GET     http://example.com/       200  12ms
14:02:12 CONNECT www.google.com:443        túnel abierto
14:02:15 CONNECT www.google.com:443        cerrado  48KB↑ 1.2MB↓  3.1s
14:02:20 GET     http://nohay.invalid/     502  dial: no such host
```

Con la bandera `-v`, además se imprimen las cabeceras completas de cada
petición y respuesta HTTP tal como pasan por el proxy (antes y después de
quitar las de salto), para ver exactamente qué cambia.

### Apagado

Ctrl+C (SIGINT) cierra el listener y termina el proceso. No espera a que
terminen las conexiones abiertas.

### Organización del código (sugerencia)

Un solo paquete `main`, dividido por responsabilidad para que cada archivo se
pueda leer solo:

- `main.go`: banderas, listener, bucle de `Accept`, señal de apagado.
- `conn.go`: `handleConn`, el bucle que lee peticiones y decide qué hacer.
- `forward.go`: reenvío HTTP y cabeceras de salto.
- `tunnel.go`: `CONNECT` y copia bidireccional.
- `logging.go`: formato de las líneas de log.

La lectura de la petición pasa por una sola función, para poder reemplazarla
después por un analizador propio (enfoque C) sin tocar el resto. El túnel vive
en su propio archivo para que la segunda etapa (leer HTTPS) lo pueda envolver o
reemplazar.

### Pruebas

Pruebas automáticas con `go test`, usando servidores de destino locales. En las
pruebas sí se permite `net/http/httptest` y `http.Client` como cliente, porque
ahí no esconden nada del proxy. Casos:

- GET simple a través del proxy.
- POST con cuerpo.
- Respuesta `chunked` desde el destino.
- Keep-alive: dos peticiones por la misma conexión al proxy.
- Las cabeceras de salto no llegan al destino.
- `CONNECT` a un servidor TLS de prueba (`httptest.NewTLSServer`).
- Destino inalcanzable → `502`.
- Petición sin URL absoluta → `400`.

Prueba manual: `curl -x http://127.0.0.1:8080 http://example.com/`, luego
`https://example.com/`, luego un navegador.

## Decisiones

**Del usuario:**

- Proxy de salida, no inverso.
- Para aprender cómo funciona por dentro.
- HTTPS solo como túnel por ahora; leer su contenido, después.
- Enfoque B.
- Conoce Go en general; las explicaciones van a lo propio del proxy.

**Propuestas por Claude (a revisar):**

- Modo mixto: Claude escribe la base y las pruebas, el usuario escribe
  conexiones, reenvío y túnel.
- Corre en local, `127.0.0.1:8080`, sin contraseña.
- Solo librería estándar.
- Formato del log de arriba y bandera `-v` para ver cabeceras.
- Una conexión nueva al destino por petición.
- Tiempos: 10 s para conectar, 60 s de inactividad del cliente, túneles sin
  límite.
- Un solo paquete `main`, un archivo por responsabilidad.
- Pruebas automáticas con servidores locales.

## A criterio de quien construye

- Nombres, estructura interna de las funciones y columnas exactas del log.
- Si agregar cabeceras propias como `Via` (no requerido).
- Casos como `HEAD` o `Expect: 100-continue`, mientras no rompan las pruebas.

## Segunda etapa (fuera de este diseño)

Leer HTTPS: autoridad de certificados propia, certificado por dominio generado
al momento, y MITM sobre el túnel.
