package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Serve accepts connections on ln and handles each one in its own
// goroutine, so a slow or idle client never holds up the others.
//
// When Accept fails with net.ErrClosed, Serve returns nil: closing the
// listener is how the proxy shuts down on Ctrl+C. Any other error is
// returned.
func (p *Proxy) Serve(ln net.Listener) error {
	defer ln.Close()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}

			return err
		}

		go p.handleConn(conn)
	}
}

// handleConn serves one client from start to finish, then closes the
// connection.
//
// A single bufio.Reader serves the whole connection, because it may already
// hold bytes that come after the current request: the next request, or the
// start of a TLS handshake after a CONNECT. Each request gets p.IdleTimeout
// to arrive. The deadline is cleared once the request is read, so a slow
// body or a long tunnel isn't cut short.
//
// A CONNECT hands the connection over to the tunnel, since it no longer
// speaks HTTP after that. Any other method is forwarded, and the loop goes
// on while forward reports that the connection can stay open.
//
// A client that hangs up or stays idle is closed without logging: browsers
// open spare connections and drop them. Any other read error gets a 400.
func (p *Proxy) handleConn(client net.Conn) {
	buf := bufio.NewReader(client)

	defer client.Close()

	for {
		client.SetReadDeadline(time.Now().Add(p.IdleTimeout))

		req, err := readRequest(buf)
		if err != nil {
			var netErr net.Error
			isTimeout := errors.As(err, &netErr) && netErr.Timeout()

			if errors.Is(err, io.EOF) || isTimeout {
				return
			}

			p.Log.Errorf("malformed request from %s: %v", client.RemoteAddr(), err)

			writeError(client, http.StatusBadRequest, "malformed request")

			return
		}

		client.SetReadDeadline(time.Time{})

		if req.Method != http.MethodConnect {
			if !p.forward(client, req) {
				return
			}
		} else {
			p.tunnel(client, buf, req)

			return
		}
	}
}

// readRequest reads one HTTP request from br. It's the only place where the
// client's HTTP is parsed, so it can later be swapped for a hand-written
// parser without touching the rest.
func readRequest(br *bufio.Reader) (*http.Request, error) {
	return http.ReadRequest(br)
}

// writeError writes a complete error response by hand: the status line,
// Content-Type, Content-Length and Connection: close headers, a blank line
// and msg as the body. Every line ends in \r\n.
//
// The proxy always closes the client connection after an error response:
// part of the request body may still be unread in the buffer, and reading
// it as the next request would be worse.
func writeError(w io.Writer, status int, msg string) error {
	if _, err := fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", status, http.StatusText(status), len(msg), msg); err != nil {
		return err
	}

	return nil
}
