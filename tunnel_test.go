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

// --- Tunnel helpers ---

// echoServer sends back every byte it receives and closes when the other
// side finishes sending.
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

// connect opens a tunnel through the proxy to target. extra travels in the
// same write as the CONNECT, as clients do when they send the TLS handshake
// without waiting for the 200.
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

// readN reads exactly n bytes from the tunnel.
func readN(t *testing.T, br *bufio.Reader, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatalf("leyendo %d bytes del túnel: %v (llegó %q)", n, err, buf)
	}
	return string(buf)
}

// --- Tunnel ---

func TestTunnelHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hola por https")
	}))
	t.Cleanup(srv.Close)
	addr, logs := startProxy(t, nil)
	// The Transport from srv.Client() trusts the test certificate.
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

	// Closing the client connection has to end the tunnel.
	client.CloseIdleConnections()
	waitForLog(t, logs, "cerrado")
}

func TestTunnelKeepsBufferedBytes(t *testing.T) {
	addr, _ := startProxy(t, nil)
	// "PING" arrives together with the CONNECT, so handleConn's bufio.Reader
	// has already read it from the socket. If the tunnel copied from the raw
	// connection instead of that bufio.Reader, "PING" would be lost.
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

	// We close only our write half. The proxy has to pass that EOF on to the
	// target; the echo then closes, and that close has to travel back to us.
	conn.(*net.TCPConn).CloseWrite()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	expectClosed(t, br)
	waitForLog(t, logs, "4B↑ 4B↓")
}

func TestTunnelMissingPort400(t *testing.T) {
	for _, target := range []string{"nohost", "nohost:"} {
		t.Run(target, func(t *testing.T) {
			// If the proxy tried to connect, this Dial would fail and the
			// response would be 502 instead of 400.
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

// A tunnel can go a long time without traffic (an open tab): the idle
// timeout between HTTP requests doesn't apply to it.
func TestTunnelOutlivesIdleTimeout(t *testing.T) {
	addr, _ := startProxy(t, func(p *Proxy) { p.IdleTimeout = 200 * time.Millisecond })
	conn, br := connect(t, addr, echoServer(t), "")

	time.Sleep(500 * time.Millisecond)
	io.WriteString(conn, "sigo aquí")
	if got := readN(t, br, len("sigo aquí")); got != "sigo aquí" {
		t.Errorf("el eco devolvió %q, quiero %q", got, "sigo aquí")
	}
}

// --- Edge cases ---

func TestTunnelServerClosesFirst(t *testing.T) {
	// A target that greets and closes right away, without waiting for the
	// client.
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
	// The target's close has to reach us as EOF even though we keep our side
	// of the connection open.
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	expectClosed(t, br)

	// Only when we close does the other copy finish and the tunnel get
	// logged as closed.
	conn.Close()
	waitForLog(t, logs, "cerrado  0B↑ 5B↓")
}
