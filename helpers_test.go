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

// syncBuffer is a bytes.Buffer that is safe to use from several
// goroutines: the proxy writes the log from each connection's goroutine
// while the test reads it.
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

// startProxy starts a proxy on a free port and returns its address and its
// log. The timeouts are shorter than the real ones, so a failing test
// doesn't hang for a minute.
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

// proxyClient returns an http.Client that sends everything through the
// proxy. base lets it start from an already configured Transport (for
// example, the one that trusts an httptest.NewTLSServer certificate).
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

// rawConn opens a bare TCP connection, to write HTTP by hand and see
// exactly what the proxy answers.
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

// closedAddr returns an address where nothing is listening: dialing it
// gives "connection refused".
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

// waitForLog waits for substr to appear in the log. The proxy logs from
// another goroutine, sometimes after the client has already received the
// response.
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

// readBody reads and closes a complete body.
func readBody(t *testing.T, r io.ReadCloser) string {
	t.Helper()
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("leyendo el cuerpo: %v", err)
	}
	return string(b)
}

// expectClosed requires the other side to have closed the connection: the
// next byte has to be EOF.
func expectClosed(t *testing.T, br *bufio.Reader) {
	t.Helper()
	b, err := br.ReadByte()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("esperaba EOF (conexión cerrada), obtuve byte %q, error %v", b, err)
	}
}
