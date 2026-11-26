package main

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

// hopByHopHeaders lists the headers that describe a single connection
// (client ↔ proxy, or proxy ↔ target) rather than the message, so a proxy
// must not pass them on (RFC 9110 §7.6.1).
var hopByHopHeaders = [9]string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

// removeHopByHop deletes the hop-by-hop headers from h: first every header
// named in the Connection values (comma-separated), then the fixed list in
// hopByHopHeaders. The order matters: Connection is in that list, and
// deleting it first would lose the names it carries.
func removeHopByHop(h http.Header) {
	conns := h["Connection"]
	for _, conn := range conns {
		for otherConns := range strings.SplitSeq(conn, ",") {
			h.Del(strings.TrimSpace(otherConns))
		}
	}

	for _, headers := range hopByHopHeaders {
		h.Del(headers)
	}
}

// forward sends a plain HTTP request to its target and copies the response
// back to the client. It returns whether the client connection can stay open
// for the next request (keep-alive).
//
// Every request gets a new connection to the target, which is asked to close
// it afterwards. That's the simplest flow to follow, at the cost of a TCP
// handshake per request. Errors are answered by the proxy itself: 400 for a
// URL that isn't absolute http://, 502 or 504 when the target can't be
// reached, and 502 when it doesn't send a valid response.
func (p *Proxy) forward(client net.Conn, req *http.Request) (keepAlive bool) {
	start := time.Now()
	target := req.URL.String()
	if req.URL.Scheme != "http" || req.URL.Host == "" {
		msg := "se esperaba una URL absoluta http://"

		writeError(client, http.StatusBadRequest, msg)

		p.Log.HTTPError(req.Method, target, http.StatusBadRequest, errors.New(msg))

		return keepAlive
	}

	host, port := req.URL.Hostname(), req.URL.Port()
	if port == "" {
		port = "80"
	}

	addr, clientClose := net.JoinHostPort(host, port), req.Close

	if p.Verbose {
		p.Log.Headers("petición del cliente", req.Header)
	}

	removeHopByHop(req.Header)

	if p.Verbose {
		p.Log.Headers("petición al destino", req.Header)
	}

	// One connection per request: Connection: close tells the target to close
	// it once the response is done. Whether the client connection stays open
	// is decided separately, with clientClose.
	req.Close = true

	up, err := p.dial(addr)
	if err != nil {
		status := statusForDialError(err)

		writeError(client, status, err.Error())

		p.Log.HTTPError(req.Method, target, status, err)

		return keepAlive
	}
	defer up.Close()

	// Write sends the request in origin form ("GET /path HTTP/1.1"), which is
	// how you talk to a server. WriteProxy would send it in absolute form
	// ("GET http://host/path HTTP/1.1"), which is how the client talked to the
	// proxy. Going from one form to the other is part of the proxy's job.
	if err := req.Write(up); err != nil {
		writeError(client, http.StatusBadGateway, err.Error())

		p.Log.HTTPError(req.Method, target, http.StatusBadGateway, err)

		return keepAlive
	}

	upBr := bufio.NewReader(up)

	// Passing req lets ReadResponse know that a response to HEAD has no body.
	// Interim 1xx responses are skipped: with Expect: 100-continue the target
	// first sends 100 Continue and then the real response. 101 isn't interim:
	// it switches the connection to another protocol.
	resp, err := http.ReadResponse(upBr, req)
	for err == nil && resp.StatusCode >= 100 && resp.StatusCode <= 199 && resp.StatusCode != 101 {
		resp, err = http.ReadResponse(upBr, req)
	}

	if err != nil {
		writeError(client, http.StatusBadGateway, err.Error())

		p.Log.HTTPError(req.Method, target, http.StatusBadGateway, err)

		return keepAlive
	}
	defer resp.Body.Close()

	if p.Verbose {
		p.Log.Headers("respuesta del destino", resp.Header)
	}

	removeHopByHop(resp.Header)

	if p.Verbose {
		p.Log.Headers("respuesta al cliente", resp.Header)
	}

	// TransferEncoding is empty in most responses: check its length before
	// reading element 0.
	chunked := len(resp.TransferEncoding) > 0 && resp.TransferEncoding[0] == "chunked"

	// The connection can stay open if the client didn't ask to close it and
	// can tell where the body ends (by its length, or because chunked marks
	// its own end). Otherwise, only closing the connection can mark the end of
	// the body.
	keepAlive = !clientClose && (resp.ContentLength >= 0 || chunked)

	// The target's Connection: close is about its connection with the proxy,
	// not the client's: it's overridden with the decision above.
	resp.Close = !keepAlive

	if err := resp.Write(client); err != nil {
		p.Log.Errorf("escribiendo la respuesta a %s: %v", client.RemoteAddr(), err)

		return false
	}

	p.Log.HTTP(req.Method, target, resp.StatusCode, time.Since(start))

	return keepAlive
}
