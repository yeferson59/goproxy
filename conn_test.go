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
	if err := writeError(&buf, 502, "could not connect"); err != nil {
		t.Fatalf("writeError: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "HTTP/1.1 502 Bad Gateway\r\n") {
		t.Fatalf("status line: %q", buf.String())
	}
	raw := buf.String()
	resp, err := http.ReadResponse(bufio.NewReader(&buf), nil)
	if err != nil {
		t.Fatalf("http.ReadResponse can't parse the response: %v\nraw: %q", err, raw)
	}
	if resp.StatusCode != 502 {
		t.Errorf("StatusCode = %d, want 502", resp.StatusCode)
	}
	if !resp.Close {
		t.Errorf("missing Connection: close")
	}
	if got := resp.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if resp.ContentLength != int64(len("could not connect")) {
		t.Errorf("ContentLength = %d, want %d", resp.ContentLength, len("could not connect"))
	}
	if body := readBody(t, resp.Body); body != "could not connect" {
		t.Errorf("body = %q", body)
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
			t.Fatalf("Serve returned %v, want nil after the listener closed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve didn't return after the listener closed")
	}
}

func TestServeHandlesClientsConcurrently(t *testing.T) {
	addr, _ := startProxy(t, nil)

	// The first client connects and sends nothing: its goroutine stays
	// waiting for a request.
	rawConn(t, addr)

	// The second one has to get a response anyway. If Serve accepted only
	// one connection, or served clients one at a time (without go), it would
	// never get one.
	conn, br := rawConn(t, addr)
	conn.SetDeadline(time.Now().Add(time.Second))
	io.WriteString(conn, "this is not http\r\n\r\n")
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("second client got no response while the first was still connected: %v", err)
	}
	readBody(t, resp.Body)
	if resp.StatusCode != 400 {
		t.Errorf("StatusCode = %d, want 400", resp.StatusCode)
	}
}

func TestMalformedRequestGets400(t *testing.T) {
	addr, logs := startProxy(t, nil)
	conn, br := rawConn(t, addr)
	io.WriteString(conn, "this is not http\r\n\r\n")

	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("StatusCode = %d, want 400", resp.StatusCode)
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
		t.Errorf("proxy took %v to close an idle client; IdleTimeout was 200ms", elapsed)
	}
}

func TestClientHangUpIsQuiet(t *testing.T) {
	addr, logs := startProxy(t, nil)
	conn, _ := rawConn(t, addr)
	conn.Close()
	time.Sleep(200 * time.Millisecond)

	// Closing without sending anything is what a browser does with the
	// spare connections it opened: it's not an error.
	if strings.Contains(logs.String(), "ERROR") {
		t.Errorf("closing without sending anything shouldn't log an error; log:\n%s", logs.String())
	}
}
