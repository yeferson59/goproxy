// goproxy es un proxy de salida (forward proxy) HTTP escrito para aprender
// cómo funciona un proxy por dentro: acepta conexiones TCP, reenvía peticiones
// HTTP en texto claro y abre túneles CONNECT para HTTPS.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "dirección donde escucha el proxy")
	verbose := flag.Bool("v", false, "muestra las cabeceras de cada petición y respuesta")
	flag.Parse()

	// Por defecto solo 127.0.0.1: escuchar en todas las interfaces dejaría un
	// proxy abierto a cualquiera en la red.
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "no se pudo escuchar:", err)
		os.Exit(1)
	}

	p := NewProxy(NewLogger(os.Stdout))
	p.Verbose = *verbose

	// Cerrar el listener hace que Accept falle con net.ErrClosed, y Serve lo
	// interpreta como un apagado normal. No se espera a las conexiones que
	// siguen abiertas: el proceso termina y el sistema las cierra.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		ln.Close()
	}()

	fmt.Println("goproxy escuchando en", ln.Addr())
	if err := p.Serve(ln); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
