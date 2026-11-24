package main

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestWriteError(t *testing.T) {
	var buf bytes.Buffer
	if err := writeError(&buf, 502, "no se pudo conectar"); err != nil {
		t.Fatalf("writeError: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "HTTP/1.1 502 Bad Gateway\r\n") {
		t.Fatalf("línea de estado: %q", buf.String())
	}
	raw := buf.String()
	resp, err := http.ReadResponse(bufio.NewReader(&buf), nil)
	if err != nil {
		t.Fatalf("http.ReadResponse no entiende la respuesta: %v\nen crudo: %q", err, raw)
	}
	if resp.StatusCode != 502 {
		t.Errorf("StatusCode = %d, quiero 502", resp.StatusCode)
	}
	if !resp.Close {
		t.Errorf("falta Connection: close")
	}
	if got := resp.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if resp.ContentLength != int64(len("no se pudo conectar")) {
		t.Errorf("ContentLength = %d, quiero %d", resp.ContentLength, len("no se pudo conectar"))
	}
	if body := readBody(t, resp.Body); body != "no se pudo conectar" {
		t.Errorf("cuerpo = %q", body)
	}
}

func TestServeReturnsWhenListenerCloses(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := NewProxy(NewLogger(io.Discard))
	done := make(chan error, 1)
	go func() { done <- p.Serve(ln) }()

	ln.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve devolvió %v, quiero nil al cerrar el listener", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve no volvió después de cerrar el listener")
	}
}

func TestServeHandlesClientsConcurrently(t *testing.T) {
	addr, _ := startProxy(t, nil)

	// El primer cliente se conecta y no manda nada: su goroutine se queda
	// esperando una petición.
	rawConn(t, addr)

	// El segundo tiene que recibir respuesta de todos modos. Si Serve
	// aceptara una sola conexión, o atendiera a los clientes en fila (sin
	// go), este nunca la recibiría.
	conn, br := rawConn(t, addr)
	conn.SetDeadline(time.Now().Add(time.Second))
	io.WriteString(conn, "esto no es http\r\n\r\n")
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("el segundo cliente no recibió respuesta mientras el primero seguía conectado: %v", err)
	}
	readBody(t, resp.Body)
	if resp.StatusCode != 400 {
		t.Errorf("StatusCode = %d, quiero 400", resp.StatusCode)
	}
}

func TestMalformedRequestGets400(t *testing.T) {
	addr, logs := startProxy(t, nil)
	conn, br := rawConn(t, addr)
	io.WriteString(conn, "esto no es http\r\n\r\n")

	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("leyendo la respuesta: %v", err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("StatusCode = %d, quiero 400", resp.StatusCode)
	}
	readBody(t, resp.Body)
	expectClosed(t, br)
	waitForLog(t, logs, "ERROR")
}

func TestIdleClientIsClosed(t *testing.T) {
	addr, _ := startProxy(t, func(p *Proxy) { p.IdleTimeout = 200 * time.Millisecond })
	_, br := rawConn(t, addr)

	start := time.Now()
	expectClosed(t, br)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("el proxy tardó %v en cerrar un cliente inactivo; IdleTimeout era 200ms", elapsed)
	}
}

func TestClientHangUpIsQuiet(t *testing.T) {
	addr, logs := startProxy(t, nil)
	conn, _ := rawConn(t, addr)
	conn.Close()
	time.Sleep(200 * time.Millisecond)

	// Cerrar sin mandar nada es lo que hace un navegador con las conexiones
	// que abrió de más: no es un error.
	if strings.Contains(logs.String(), "ERROR") {
		t.Errorf("cerrar sin mandar nada no debería registrar un error; log:\n%s", logs.String())
	}
}
