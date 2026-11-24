package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
)

// Serve acepta conexiones en ln y atiende cada una en su propia goroutine
// (go p.handleConn(conn)).
//
// Si Accept falla con net.ErrClosed (errors.Is), devuelve nil: así se apaga
// el proxy con Ctrl+C. Con cualquier otro error, lo devuelve.
//
// Tarea 3, paso 5.
func (p *Proxy) Serve(ln net.Listener) error {
	panic("sin implementar: Tarea 3")
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
	panic("sin implementar: Tarea 3")
}

// readRequest lee una petición HTTP de br. Es el único lugar donde se analiza
// HTTP del cliente, para poder cambiarlo después por un analizador propio.
//
// Tarea 3, paso 4.
func readRequest(br *bufio.Reader) (*http.Request, error) {
	panic("sin implementar: Tarea 3")
}

// writeError escribe a mano una respuesta de error completa: línea de estado
// "HTTP/1.1 <código> <texto>", cabeceras Content-Type (text/plain;
// charset=utf-8), Content-Length y Connection: close, una línea vacía y msg.
// Cada línea termina en \r\n.
//
// Tarea 3, paso 3.
func writeError(w io.Writer, status int, msg string) error {
	panic("sin implementar: Tarea 3")
}
