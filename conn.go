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

// Serve acepta conexiones en ln y atiende cada una en su propia goroutine
// (go p.handleConn(conn)).
//
// Si Accept falla con net.ErrClosed (errors.Is), devuelve nil: así se apaga
// el proxy con Ctrl+C. Con cualquier otro error, lo devuelve.
//
// Tarea 3, paso 5.
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

// handleConn atiende a un cliente de principio a fin:
//
//  1. defer client.Close().
//  2. Un solo bufio.Reader para toda la conexión.
//  3. En bucle: plazo de lectura de p.IdleTimeout, readRequest, quitar el
//     plazo, y luego CONNECT → p.tunnel y terminar; otro método → p.forward,
//     y seguir solo si devuelve true.
//
// EOF o tiempo agotado al esperar una petición: terminar sin registrar.
// Cualquier otro error de lectura: p.Log.Errorf, writeError 400 y terminar.
//
// Tarea 3, paso 6.
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

			p.Log.Errorf("petición mal formada de %s: %v", client.RemoteAddr(), err)

			writeError(client, http.StatusBadRequest, "petición mal formada")

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

// readRequest lee una petición HTTP de br. Es el único lugar donde se analiza
// HTTP del cliente, para poder cambiarlo después por un analizador propio.
//
// Tarea 3, paso 4.
func readRequest(br *bufio.Reader) (*http.Request, error) {
	return http.ReadRequest(br)
}

// writeError escribe a mano una respuesta de error completa: línea de estado
// "HTTP/1.1 <código> <texto>", cabeceras Content-Type (text/plain;
// charset=utf-8), Content-Length y Connection: close, una línea vacía y msg.
// Cada línea termina en \r\n.
//
// Tarea 3, paso 3.
func writeError(w io.Writer, status int, msg string) error {
	if _, err := fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", status, http.StatusText(status), len(msg), msg); err != nil {
		return err
	}

	return nil
}
