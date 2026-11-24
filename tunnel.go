package main

import (
	"bufio"
	"net"
	"net/http"
)

// tunnel atiende un CONNECT: conecta con req.Host ("host:puerto"; sin puerto
// → 400), responde "HTTP/1.1 200 Connection Established\r\n\r\n" y copia
// bytes en las dos direcciones hasta que ambos lados terminan. La subida
// copia desde br, no desde client, para no perder bytes que ya estaban en el
// búfer. Cada copia, al terminar, cierra solo la mitad de escritura del otro
// lado (CloseWrite). handleConn cierra client al volver.
//
// Tarea 6, paso 3.
func (p *Proxy) tunnel(client net.Conn, br *bufio.Reader, req *http.Request) {
	panic("sin implementar: Tarea 6")
}
