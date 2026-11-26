package main

import (
	"errors"
	"net"
	"net/http"
	"time"
)

// Proxy guarda la configuración compartida por todas las conexiones.
type Proxy struct {
	// Log recibe una línea por petición o túnel.
	Log *Logger
	// Verbose activa el volcado de cabeceras en cada salto (-v).
	Verbose bool
	// DialTimeout limita cuánto se espera al conectar con el destino.
	DialTimeout time.Duration
	// IdleTimeout cierra la conexión con un cliente que no manda una nueva
	// petición en ese tiempo.
	IdleTimeout time.Duration
	// Dial reemplaza la conexión al destino. Si es nil se usa TCP real, las
	// pruebas lo cambian para simular fallos que son difíciles de provocar,
	// como un tiempo agotado.
	Dial func(addr string) (net.Conn, error)
}

// NewProxy crea un Proxy con los tiempos del diseño.
func NewProxy(log *Logger) *Proxy {
	return new(Proxy{
		Log:         log,
		DialTimeout: 10 * time.Second,
		IdleTimeout: 60 * time.Second,
	})
}

// dial abre la conexión TCP hacia el destino addr ("host:puerto").
func (p *Proxy) dial(addr string) (net.Conn, error) {
	if p.Dial != nil {
		return p.Dial(addr)
	}

	return net.DialTimeout("tcp", addr, p.DialTimeout)
}

// statusForDialError traduce un fallo al conectar con el destino al código que
// ve el cliente: 504 si se agotó el tiempo, 502 para todo lo demás (DNS,
// conexión rechazada, red inalcanzable).
func statusForDialError(err error) int {
	// net.Error es la interfaz que implementan los errores de red de Go;
	// Timeout() distingue "el destino no respondió a tiempo" de "el destino
	// dijo que no" o "el nombre no existe".
	var netErr net.Error

	if errors.As(err, &netErr) && netErr.Timeout() {
		return http.StatusGatewayTimeout
	}

	return http.StatusBadGateway
}
