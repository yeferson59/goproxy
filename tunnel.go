package main

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

func closeWrite(c net.Conn) {
	v, ok := c.(interface{ CloseWrite() error })
	if !ok {
		return
	}

	v.CloseWrite()
}

// tunnel atiende un CONNECT: conecta con req.Host ("host:puerto"; sin puerto
// → 400), responde "HTTP/1.1 200 Connection Established\r\n\r\n" y copia
// bytes en las dos direcciones hasta que ambos lados terminan. La subida
// copia desde br, no desde client, para no perder bytes que ya estaban en el
// búfer. Cada copia, al terminar, cierra solo la mitad de escritura del otro
// lado (CloseWrite). handleConn cierra client al volver.
//
// Tarea 6, paso 3.
func (p *Proxy) tunnel(client net.Conn, br *bufio.Reader, req *http.Request) {
	target := req.Host
	host, port, err := net.SplitHostPort(req.Host)
	if err != nil {
		writeError(client, 400, "CONNECT necesita host:puerto")

		p.Log.HTTPError(http.MethodConnect, target, 400, err)

		return
	}

	if port == "" {
		msg := "falta el puerto"

		writeError(client, 400, msg)

		p.Log.HTTPError(http.MethodConnect, target, 400, errors.New(msg))

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

	client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

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
