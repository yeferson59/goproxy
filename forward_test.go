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
	// Connection names X-Secret: even though it isn't in the fixed list, it's
	// also hop-by-hop for this message.
	h.Set("Connection", "X-Secret, Keep-Alive")
	for _, k := range []string{"X-Secret", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Set(k, "x")
	}
	h.Set("X-Normal", "ok")

	removeHopByHop(h)

	if len(h) != 1 || h.Get("X-Normal") != "ok" {
		t.Errorf("only X-Normal should remain; got %v", h)
	}
}

func TestRemoveHopByHopMultipleConnectionHeaders(t *testing.T) {
	// Connection can be repeated, and each value can carry several
	// comma-separated names.
	h := http.Header{
		"Connection": {"A", "B, C"},
		"A":          {"1"},
		"B":          {"2"},
		"C":          {"3"},
		"X-Normal":   {"ok"},
	}

	removeHopByHop(h)

	if len(h) != 1 || h.Get("X-Normal") != "ok" {
		t.Errorf("only X-Normal should remain; got %v", h)
	}
}

// --- Forwarding helpers ---

// origin is a target server that remembers the last request it received,
// to check what the proxy sent it.
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

		w.Header().Set("X-Upstream", "yes")
		// Keep-Alive is hop-by-hop: the proxy shouldn't pass it on to the client.
		w.Header().Set("Keep-Alive", "timeout=5")
		switch r.URL.Path {
		case "/echo":
			// Read everything before responding: Go's HTTP/1 server doesn't
			// guarantee it can keep reading the body once it has started
			// writing the response.
			body, _ := io.ReadAll(r.Body)
			w.Write(body)
		case "/chunked":
			// Flush sends the first part before the total length is known, so
			// Go responds with Transfer-Encoding: chunked.
			io.WriteString(w, "part1")
			w.(http.Flusher).Flush()
			io.WriteString(w, "part2")
		default:
			io.WriteString(w, "home")
		}
	}))
	t.Cleanup(o.Close)
	return o
}

// last returns the path and headers of the last request received.
func (o *origin) last() (uri string, h http.Header) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.uri, o.header
}

// noLengthOrigin is a target that responds with neither Content-Length nor
// chunked: closing the connection marks the end of the body, as in HTTP/1.0.
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
				// Read the complete headers before closing: closing with unread
				// data makes the OS send RST instead of FIN, and the proxy
				// could lose the response.
				br := bufio.NewReader(conn)
				for {
					line, err := br.ReadString('\n')
					if err != nil || line == "\r\n" {
						break
					}
				}
				io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\nbody without length")
			}()
		}
	}()
	return ln.Addr().String()
}

// readResponse reads a response from the proxy. method tells Go whether
// the request was HEAD, because that response has no body.
func readResponse(t *testing.T, br *bufio.Reader, method string) *http.Response {
	t.Helper()
	resp, err := http.ReadResponse(br, &http.Request{Method: method})
	if err != nil {
		t.Fatalf("reading proxy response: %v", err)
	}
	return resp
}

// --- Forwarding ---

func TestForwardGET(t *testing.T) {
	o := newOrigin(t)
	addr, logs := startProxy(t, nil)

	resp, err := proxyClient(t, addr, nil).Get(o.URL + "/?x=1")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
	if body := readBody(t, resp.Body); body != "home" {
		t.Errorf("body = %q, want %q", body, "home")
	}
	if got := resp.Header.Get("X-Upstream"); got != "yes" {
		t.Errorf("X-Upstream = %q: the proxy dropped a regular header from the target", got)
	}
	if got := resp.Header.Get("Keep-Alive"); got != "" {
		t.Errorf("Keep-Alive = %q: it's hop-by-hop and shouldn't reach the client", got)
	}

	// Origin form: the target sees only the path, never "http://...".
	if uri, _ := o.last(); uri != "/?x=1" {
		t.Errorf("target got path %q, want %q", uri, "/?x=1")
	}

	waitForLog(t, logs, o.URL+"/?x=1")
	if !strings.Contains(logs.String(), " 200  ") {
		t.Errorf("log doesn't show status 200:\n%s", logs.String())
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
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
	if got := readBody(t, resp.Body); got != string(body) {
		t.Errorf("echo has %d bytes, want the %d bytes sent", len(got), len(body))
	}
}

func TestForwardChunked(t *testing.T) {
	o := newOrigin(t)
	addr, _ := startProxy(t, nil)
	conn, br := rawConn(t, addr)

	io.WriteString(conn, "GET "+o.URL+"/chunked HTTP/1.1\r\nHost: x\r\n\r\n")
	resp := readResponse(t, br, "GET")
	if len(resp.TransferEncoding) == 0 || resp.TransferEncoding[0] != "chunked" {
		t.Errorf("TransferEncoding = %v, want [chunked]", resp.TransferEncoding)
	}
	if body := readBody(t, resp.Body); body != "part1part2" {
		t.Errorf("body = %q, want %q", body, "part1part2")
	}

	// A chunked body marks its own end, so the connection can stay open.
	io.WriteString(conn, "GET "+o.URL+"/ HTTP/1.1\r\nHost: x\r\n\r\n")
	resp = readResponse(t, br, "GET")
	if body := readBody(t, resp.Body); body != "home" {
		t.Errorf("second request: body = %q, want %q", body, "home")
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
			t.Errorf("request %d: StatusCode = %d, want 200", i, resp.StatusCode)
		}
		if body := readBody(t, resp.Body); body != "home" {
			t.Errorf("request %d: body = %q, want %q", i, body, "home")
		}
		if resp.Close {
			t.Errorf("request %d: proxy sent Connection: close, but the client didn't ask for it", i)
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
	// Reading the response guarantees that the target got the request.
	resp := readResponse(t, br, "GET")
	readBody(t, resp.Body)

	_, h := o.last()
	for _, k := range []string{"X-Secret", "Proxy-Connection", "Keep-Alive", "Proxy-Authorization"} {
		if v := h.Get(k); v != "" {
			t.Errorf("target got %s: %q; it's hop-by-hop", k, v)
		}
	}
	if got := h.Get("X-Normal"); got != "ok" {
		t.Errorf("X-Normal = %q: the proxy dropped a regular header", got)
	}
	// The proxy uses a new connection per request and tells the target so.
	if got := h.Get("Connection"); got != "close" {
		t.Errorf("Connection to target = %q, want %q", got, "close")
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
		t.Errorf("StatusCode = %d, want 502", resp.StatusCode)
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
		t.Errorf("StatusCode = %d, want 504", resp.StatusCode)
	}
	waitForLog(t, logs, " 504  ")
}

func TestForwardRejectsNonAbsolute400(t *testing.T) {
	addr, _ := startProxy(t, nil)
	conn, br := rawConn(t, addr)

	// A client that sends "GET /x" thinks it's talking to a server, not to a
	// proxy.
	io.WriteString(conn, "GET /x HTTP/1.1\r\nHost: a\r\n\r\n")
	resp := readResponse(t, br, "GET")
	readBody(t, resp.Body)
	if resp.StatusCode != 400 {
		t.Errorf("StatusCode = %d, want 400", resp.StatusCode)
	}
	expectClosed(t, br)
}

func TestForwardRejectsNonHTTPScheme400(t *testing.T) {
	// If the proxy tried to connect, this Dial would fail and the response
	// would be 502 instead of 400.
	addr, _ := startProxy(t, func(p *Proxy) {
		p.Dial = func(string) (net.Conn, error) { return nil, errors.New("should not dial anywhere") }
	})
	conn, br := rawConn(t, addr)

	// HTTPS through a proxy goes over CONNECT. An https:// URL in a GET
	// can't be served without handling the encryption ourselves.
	io.WriteString(conn, "GET https://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n")
	resp := readResponse(t, br, "GET")
	readBody(t, resp.Body)
	if resp.StatusCode != 400 {
		t.Errorf("StatusCode = %d, want 400", resp.StatusCode)
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
		"--- request from client ---",
		"--- request to target ---",
		"--- response from target ---",
		"--- response to client ---",
		" 200  ",
	} {
		waitForLog(t, logs, s)
	}
	// It appears in what the client sent and in none of the later dumps.
	if n := strings.Count(logs.String(), "Proxy-Connection: keep-alive"); n != 1 {
		t.Errorf("Proxy-Connection appears %d times, want 1 (only in the client's request); log:\n%s", n, logs.String())
	}
}

// --- Edge cases ---

func TestForwardBodyWithoutLength(t *testing.T) {
	addr, _ := startProxy(t, nil)
	conn, br := rawConn(t, addr)

	io.WriteString(conn, "GET http://"+noLengthOrigin(t)+"/ HTTP/1.1\r\nHost: x\r\n\r\n")
	resp := readResponse(t, br, "GET")
	if resp.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
	// The body ends when the proxy closes. It has to close because it knows
	// it must, not because the idle timeout ran out (5 s in tests): if it
	// takes longer than 1 s, readBody fails.
	conn.SetDeadline(time.Now().Add(time.Second))
	if body := readBody(t, resp.Body); body != "body without length" {
		t.Errorf("body = %q, want %q", body, "body without length")
	}
	// Without a length, the client only knows the body ended because the
	// connection closes: the proxy has to signal it and close.
	if !resp.Close {
		t.Errorf("missing Connection: close on a response without length")
	}
	expectClosed(t, br)
}

func TestForwardSkips100Continue(t *testing.T) {
	o := newOrigin(t)
	addr, _ := startProxy(t, nil)
	conn, br := rawConn(t, addr)

	// curl sends Expect: 100-continue with bodies larger than 1 KB.
	io.WriteString(conn, "POST "+o.URL+"/echo HTTP/1.1\r\nHost: x\r\nExpect: 100-continue\r\nContent-Length: 5\r\n\r\nhello")
	resp := readResponse(t, br, "POST")
	if resp.StatusCode != 200 {
		t.Fatalf("first response is %d, want 200 (the target's 100 Continue shouldn't reach the client)", resp.StatusCode)
	}
	if body := readBody(t, resp.Body); body != "hello" {
		t.Errorf("body = %q, want %q", body, "hello")
	}
}

func TestForwardHEADKeepsConnection(t *testing.T) {
	o := newOrigin(t)
	addr, _ := startProxy(t, nil)
	conn, br := rawConn(t, addr)

	io.WriteString(conn, "HEAD "+o.URL+"/ HTTP/1.1\r\nHost: x\r\n\r\n")
	resp := readResponse(t, br, "HEAD")
	if resp.StatusCode != 200 {
		t.Errorf("HEAD: StatusCode = %d, want 200", resp.StatusCode)
	}
	if resp.ContentLength != 4 {
		t.Errorf("HEAD: ContentLength = %d, want 4 (the length a GET would have)", resp.ContentLength)
	}
	if body := readBody(t, resp.Body); body != "" {
		t.Errorf("HEAD: body = %q, want empty", body)
	}

	// If the proxy waited for the 4 body bytes of a HEAD, it would hang and
	// this second request would never get a response.
	io.WriteString(conn, "GET "+o.URL+"/ HTTP/1.1\r\nHost: x\r\n\r\n")
	resp = readResponse(t, br, "GET")
	if body := readBody(t, resp.Body); body != "home" {
		t.Errorf("GET after HEAD: body = %q, want %q", body, "home")
	}
}

func TestForwardClientConnectionClose(t *testing.T) {
	o := newOrigin(t)
	addr, _ := startProxy(t, nil)
	conn, br := rawConn(t, addr)

	io.WriteString(conn, "GET "+o.URL+"/ HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	resp := readResponse(t, br, "GET")
	if body := readBody(t, resp.Body); body != "home" {
		t.Errorf("body = %q, want %q", body, "home")
	}
	if !resp.Close {
		t.Errorf("client asked for Connection: close and the response doesn't confirm it")
	}
	// It has to close right away, not when the idle timeout runs out (5 s
	// in tests, 60 s for real).
	conn.SetDeadline(time.Now().Add(time.Second))
	expectClosed(t, br)
}

// The idle timeout only covers waiting for a new request: a body that
// arrives slowly isn't an idle client.
func TestForwardSlowBodyOutlivesIdleTimeout(t *testing.T) {
	o := newOrigin(t)
	addr, _ := startProxy(t, func(p *Proxy) { p.IdleTimeout = 200 * time.Millisecond })
	conn, br := rawConn(t, addr)

	io.WriteString(conn, "POST "+o.URL+"/echo HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n\r\nhello")
	time.Sleep(500 * time.Millisecond)
	io.WriteString(conn, "world")

	resp := readResponse(t, br, "POST")
	if resp.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
	if body := readBody(t, resp.Body); body != "helloworld" {
		t.Errorf("body = %q, want %q", body, "helloworld")
	}
}
