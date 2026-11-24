package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer es un bytes.Buffer seguro para usar desde varias goroutines: el
// proxy escribe el log desde la goroutine de cada conexión mientras la prueba
// lo lee.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startProxy arranca un proxy en un puerto libre y devuelve su dirección y su
// log. Los tiempos son más cortos que los reales para que una prueba que falla
// no se quede colgada un minuto.
func startProxy(t *testing.T, configure func(*Proxy)) (addr string, logs *syncBuffer) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logs = &syncBuffer{}
	p := NewProxy(NewLogger(logs))
	p.DialTimeout = 2 * time.Second
	p.IdleTimeout = 5 * time.Second
	if configure != nil {
		configure(p)
	}
	go p.Serve(ln)
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String(), logs
}

// proxyClient devuelve un http.Client que manda todo a través del proxy. base
// permite partir de un Transport ya configurado (por ejemplo, el que confía en
// el certificado de un httptest.NewTLSServer).
func proxyClient(t *testing.T, proxyAddr string, base *http.Transport) *http.Client {
	t.Helper()
	var tr *http.Transport
	if base != nil {
		tr = base.Clone()
	} else {
		tr = &http.Transport{}
	}
	proxyURL, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	tr.Proxy = http.ProxyURL(proxyURL)
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}
}

// rawConn abre una conexión TCP sin nada encima, para escribir HTTP a mano y
// ver exactamente qué responde el proxy.
func rawConn(t *testing.T, addr string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	t.Cleanup(func() { conn.Close() })
	return conn, bufio.NewReader(conn)
}

// closedAddr devuelve una dirección donde no escucha nadie: conectar ahí da
// "connection refused".
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// waitForLog espera a que substr aparezca en el log. El proxy registra desde
// otra goroutine, a veces después de que el cliente ya recibió la respuesta.
func waitForLog(t *testing.T, logs *syncBuffer, substr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logs.String(), substr) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("el log no contiene %q; log completo:\n%s", substr, logs.String())
}

// readBody lee y cierra un cuerpo completo.
func readBody(t *testing.T, r io.ReadCloser) string {
	t.Helper()
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("leyendo el cuerpo: %v", err)
	}
	return string(b)
}

// expectClosed exige que el otro lado haya cerrado la conexión: el siguiente
// byte tiene que ser EOF.
func expectClosed(t *testing.T, br *bufio.Reader) {
	t.Helper()
	b, err := br.ReadByte()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("esperaba EOF (conexión cerrada), obtuve byte %q, error %v", b, err)
	}
}
