// goproxy is a forward HTTP proxy written to learn how a proxy works on the
// inside: it accepts TCP connections, forwards plain HTTP requests and opens
// CONNECT tunnels for HTTPS.
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

	// Only 127.0.0.1 by default: listening on every interface would leave an
	// open proxy for anyone on the network.
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "no se pudo escuchar:", err)
		os.Exit(1)
	}

	p := NewProxy(NewLogger(os.Stdout))
	p.Verbose = *verbose

	// Closing the listener makes Accept fail with net.ErrClosed, which Serve
	// treats as a normal shutdown. Connections that are still open aren't
	// waited for: the process exits and the OS closes them.
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
