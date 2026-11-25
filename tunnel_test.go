package main

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// --- Utilidades para probar el túnel ---

// echoServer devuelve cada byte que recibe y cierra cuando el otro lado
// termina de mandar.
func echoServer(t *testing.T) string {
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
				io.Copy(conn, conn)
			}()
		}
	}()
	return ln.Addr().String()
}

// connect abre un túnel por el proxy hacia target. extra viaja en la misma
// escritura que el CONNECT, como hacen los clientes que mandan el saludo TLS
// sin esperar el 200.
func connect(t *testing.T, proxyAddr, target, extra string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, br := rawConn(t, proxyAddr)
	io.WriteString(conn, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n"+extra)
	for _, want := range []string{"HTTP/1.1 200 Connection Established\r\n", "\r\n"} {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("leyendo la respuesta al CONNECT: %v", err)
		}
		if line != want {
			t.Fatalf("respuesta al CONNECT: %q, quiero %q", line, want)
		}
	}
	return conn, br
}

// readN lee exactamente n bytes del túnel.
func readN(t *testing.T, br *bufio.Reader, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatalf("leyendo %d bytes del túnel: %v (llegó %q)", n, err, buf)
	}
	return string(buf)
}

// --- Túnel ---

func TestTunnelHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hola por https")
	}))
	t.Cleanup(srv.Close)
	addr, logs := startProxy(t, nil)
	// El Transport de srv.Client() confía en el certificado de prueba.
	client := proxyClient(t, addr, srv.Client().Transport.(*http.Transport))

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("StatusCode = %d, quiero 200", resp.StatusCode)
	}
	if body := readBody(t, resp.Body); body != "hola por https" {
		t.Errorf("cuerpo = %q, quiero %q", body, "hola por https")
	}
	waitForLog(t, logs, "túnel abierto")

	// Cerrar la conexión del cliente tiene que terminar el túnel.
	client.CloseIdleConnections()
	waitForLog(t, logs, "cerrado")
}

func TestTunnelKeepsBufferedBytes(t *testing.T) {
	addr, _ := startProxy(t, nil)
	// "PING" llega pegado al CONNECT, así que el bufio.Reader de handleConn
	// ya lo leyó del socket. Si el túnel copia desde la conexión cruda en vez
	// de desde ese bufio.Reader, "PING" se pierde.
	conn, br := connect(t, addr, echoServer(t), "PING")
	conn.SetDeadline(time.Now().Add(time.Second))

	if got := readN(t, br, 4); got != "PING" {
		t.Errorf("el eco devolvió %q, quiero %q", got, "PING")
	}
}

func TestTunnelEchoBothWays(t *testing.T) {
	addr, logs := startProxy(t, nil)
	conn, br := connect(t, addr, echoServer(t), "")

	io.WriteString(conn, "hola")
	if got := readN(t, br, 4); got != "hola" {
		t.Errorf("el eco devolvió %q, quiero %q", got, "hola")
	}

	// Cerramos solo nuestra mitad de escritura. El proxy tiene que pasarle
	// ese EOF al destino; el eco entonces cierra, y ese cierre tiene que
	// volver hasta nosotros.
	conn.(*net.TCPConn).CloseWrite()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	expectClosed(t, br)
	waitForLog(t, logs, "4B↑ 4B↓")
}

func TestTunnelMissingPort400(t *testing.T) {
	for _, target := range []string{"nohost", "nohost:"} {
		t.Run(target, func(t *testing.T) {
			// Si el proxy intentara conectarse, este Dial fallaría y la
			// respuesta sería 502 en vez de 400.
			addr, logs := startProxy(t, func(p *Proxy) {
				p.Dial = func(string) (net.Conn, error) { return nil, errors.New("no debería conectarse a ningún lado") }
			})
			conn, br := rawConn(t, addr)

			io.WriteString(conn, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
			resp := readResponse(t, br, "CONNECT")
			readBody(t, resp.Body)
			if resp.StatusCode != 400 {
				t.Errorf("StatusCode = %d, quiero 400", resp.StatusCode)
			}
			waitForLog(t, logs, " 400  ")
		})
	}
}

func TestTunnelUnreachable502(t *testing.T) {
	addr, logs := startProxy(t, nil)
	conn, br := rawConn(t, addr)
	target := closedAddr(t)

	io.WriteString(conn, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	resp := readResponse(t, br, "CONNECT")
	readBody(t, resp.Body)
	if resp.StatusCode != 502 {
		t.Errorf("StatusCode = %d, quiero 502", resp.StatusCode)
	}
	waitForLog(t, logs, " 502  ")
}

// Un túnel puede pasar mucho tiempo sin tráfico (una pestaña abierta): el
// plazo de inactividad entre peticiones HTTP no se le aplica.
func TestTunnelOutlivesIdleTimeout(t *testing.T) {
	addr, _ := startProxy(t, func(p *Proxy) { p.IdleTimeout = 200 * time.Millisecond })
	conn, br := connect(t, addr, echoServer(t), "")

	time.Sleep(500 * time.Millisecond)
	io.WriteString(conn, "sigo aquí")
	if got := readN(t, br, len("sigo aquí")); got != "sigo aquí" {
		t.Errorf("el eco devolvió %q, quiero %q", got, "sigo aquí")
	}
}

// --- Review Focus ---

func TestTunnelServerClosesFirst(t *testing.T) {
	// Un destino que saluda y cierra enseguida, sin esperar nada del cliente.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		io.WriteString(conn, "adios")
		conn.Close()
	}()
	addr, logs := startProxy(t, nil)
	conn, br := connect(t, addr, ln.Addr().String(), "")

	if got := readN(t, br, 5); got != "adios" {
		t.Errorf("llegó %q, quiero %q", got, "adios")
	}
	// El cierre del destino tiene que llegarnos como EOF aunque nosotros
	// sigamos con la conexión abierta.
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	expectClosed(t, br)

	// Recién cuando cerramos nosotros termina la otra copia y el túnel se
	// registra como cerrado.
	conn.Close()
	waitForLog(t, logs, "cerrado  0B↑ 5B↓")
}
