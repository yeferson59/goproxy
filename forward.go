package main

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
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

	req.Close = true

	up, err := p.dial(addr)
	if err != nil {
		status := statusForDialError(err)

		writeError(client, status, err.Error())

		p.Log.HTTPError(req.Method, target, status, err)

		return keepAlive
	}
	defer up.Close()

	// Write manda la petición en forma de origen ("GET /ruta HTTP/1.1"), que
	// es como se le habla a un servidor. WriteProxy la mandaría en forma
	// absoluta ("GET http://host/ruta HTTP/1.1"), que es como el cliente le
	// habló al proxy. Pasar de una forma a la otra es parte del trabajo del
	// proxy.
	if err := req.Write(up); err != nil {
		writeError(client, http.StatusBadGateway, err.Error())

		p.Log.HTTPError(req.Method, target, http.StatusBadGateway, err)

		return keepAlive
	}

	upBr := bufio.NewReader(up)

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

	// TransferEncoding viene vacío en casi todas las respuestas: hay que
	// mirar el largo antes de pedir el elemento 0.
	chunked := len(resp.TransferEncoding) > 0 && resp.TransferEncoding[0] == "chunked"

	// Se puede seguir si el cliente no pidió cerrar y sabe dónde termina el
	// cuerpo (por su largo, o porque chunked marca su propio final). Sin
	// eso, el final del cuerpo solo se puede marcar cerrando la conexión.
	keepAlive = !clientClose && (resp.ContentLength >= 0 || chunked)

	// El Connection: close del destino habla de su conexión con el proxy, no
	// de la del cliente: se pisa con lo que decidimos arriba.
	resp.Close = !keepAlive

	if err := resp.Write(client); err != nil {
		p.Log.Errorf("escribiendo la respuesta a %s: %v", client.RemoteAddr(), err)

		return false
	}

	p.Log.HTTP(req.Method, target, resp.StatusCode, time.Since(start))

	return keepAlive
}
