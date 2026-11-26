package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// closeWrite closes only the write half of c, when the connection supports
// it (TCP does). The other side gets EOF but can keep sending in the other
// direction, so a response still on its way isn't cut off.
func closeWrite(c net.Conn) {
	v, ok := c.(interface{ CloseWrite() error })
	if !ok {
		return
	}

	v.CloseWrite()
}

// tunnel serves a CONNECT: it dials req.Host ("host:port"; without a port
// the client gets 400), replies "200 Connection Established" and copies raw
// bytes in both directions until both sides are done. It never looks at the
// bytes, so the TLS session runs end to end between the client and the
// target.
//
// The upstream copy reads from br, not from client: br may already hold the
// start of the TLS handshake, sent together with the CONNECT line. When a
// copy finishes, it closes only the write half of the other side. Tunnels
// have no idle timeout, because a browser tab can keep one open for a long
// time. handleConn closes client when tunnel returns.
func (p *Proxy) tunnel(client net.Conn, br *bufio.Reader, req *http.Request) {
	target := req.Host
	host, port, err := net.SplitHostPort(req.Host)
	if err != nil {
		writeError(client, http.StatusBadRequest, "CONNECT necesita host:puerto")

		p.Log.HTTPError(http.MethodConnect, target, http.StatusBadRequest, err)

		return
	}

	if port == "" {
		msg := "falta el puerto"

		writeError(client, http.StatusBadRequest, msg)

		p.Log.HTTPError(http.MethodConnect, target, http.StatusBadRequest, errors.New(msg))

		return
	}

	addr := net.JoinHostPort(host, port)

	up, err := p.dial(addr)
	if err != nil {
		status := statusForDialError(err)

		writeError(client, status, err.Error())

		p.Log.HTTPError(req.Method, target, status, err)

		return
	}
	defer up.Close()

	if _, err := client.Write(fmt.Appendf([]byte{}, "HTTP/1.1 %d Connection Established\r\n\r\n", http.StatusOK)); err != nil {
		p.Log.HTTPError(req.Method, target, http.StatusInternalServerError, err)

		return
	}

	p.Log.TunnelOpen(target)

	start := time.Now()

	var upBytes, downBytes int64
	var wg sync.WaitGroup

	wg.Go(func() {
		downBytes, _ = io.Copy(client, up)

		closeWrite(client)
	})

	wg.Go(func() {
		upBytes, _ = io.Copy(up, br)

		closeWrite(up)
	})

	wg.Wait()

	p.Log.TunnelClosed(target, upBytes, downBytes, time.Since(start))
}
