package main

import (
	"net"
	"net/http"
	"strings"
)

var hopByHopHeaders [9]string = [9]string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

// removeHopByHop borra de h las cabeceras de salto: primero las que nombra
// cada valor de "Connection" (separadas por coma), después las de una lista
// fija (hopByHopHeaders: Connection, Proxy-Connection, Keep-Alive,
// Proxy-Authenticate, Proxy-Authorization, Te, Trailer, Transfer-Encoding,
// Upgrade).
//
// Tarea 4, paso 3.
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

// forward reenvía una petición HTTP en texto claro al destino y copia la
// respuesta al cliente. Devuelve si la conexión con el cliente puede seguir
// para la siguiente petición (keep-alive).
//
// Resumen: validar URL absoluta http:// (si no, 400), calcular host:puerto,
// quitar cabeceras de salto, req.Close = true, conectar (502/504 si falla),
// req.Write, leer la respuesta saltando las 1xx, quitar cabeceras de salto,
// decidir keep-alive, resp.Write al cliente y registrar.
//
// Tarea 5, paso 3.
func (p *Proxy) forward(client net.Conn, req *http.Request) (keepAlive bool) {
	panic("sin implementar: Tarea 5")
}
