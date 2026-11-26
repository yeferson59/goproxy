package main

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Logger escribe una línea por evento del proxy.
//
// Cada conexión corre en su propia goroutine, así que varias pueden registrar
// a la vez: el mutex evita que dos líneas se mezclen a la mitad.
type Logger struct {
	mu  sync.Mutex
	out io.Writer
	// now se puede reemplazar en las pruebas para fijar la hora.
	now func() time.Time
}

// NewLogger crea un Logger que escribe en out.
func NewLogger(out io.Writer) *Logger {
	return &Logger{out: out, now: time.Now}
}

// line escribe "hora MÉTODO destino resto" con columnas alineadas.
func (l *Logger) line(method, target, rest string) {
	l.mu.Lock()

	defer l.mu.Unlock()

	fmt.Fprintf(l.out, "%s %-7s %-25s %s\n", l.now().Format("15:04:05"), method, target, rest)
}

// HTTP registra una petición reenviada con éxito.
func (l *Logger) HTTP(method, target string, status int, elapsed time.Duration) {
	l.line(method, target, fmt.Sprintf("%d  %s", status, formatDuration(elapsed)))
}

// HTTPError registra una petición que terminó en una respuesta de error del
// propio proxy (400, 502, 504).
func (l *Logger) HTTPError(method, target string, status int, err error) {
	l.line(method, target, fmt.Sprintf("%d  %v", status, err))
}

// TunnelOpen registra que un túnel CONNECT quedó establecido.
func (l *Logger) TunnelOpen(target string) {
	l.line("CONNECT", target, "túnel abierto")
}

// TunnelClosed registra el cierre de un túnel: bytes subidos (cliente →
// destino), bytes bajados (destino → cliente) y cuánto duró.
func (l *Logger) TunnelClosed(target string, up, down int64, elapsed time.Duration) {
	l.line("CONNECT", target, fmt.Sprintf("cerrado  %s↑ %s↓  %s", formatBytes(up), formatBytes(down), formatDuration(elapsed)))
}

// Headers vuelca las cabeceras bajo un título, una línea por valor. Se usa con
// -v para ver qué cambia en cada salto.
func (l *Logger) Headers(title string, h http.Header) {
	// Un map de Go no tiene orden: ordenar las claves hace que dos volcados
	// de las mismas cabeceras se vean iguales y se puedan comparar a ojo.
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	l.mu.Lock()

	defer l.mu.Unlock()

	fmt.Fprintf(l.out, "    --- %s ---\n", title)

	for _, k := range keys {
		for _, v := range h[k] {
			fmt.Fprintf(l.out, "    %s: %s\n", k, v)
		}
	}
}

// Errorf registra un error que no corresponde a una petición concreta.
func (l *Logger) Errorf(format string, args ...any) {
	l.mu.Lock()

	defer l.mu.Unlock()

	fmt.Fprintf(l.out, "%s %-7s %s\n", l.now().Format("15:04:05"), "ERROR", fmt.Sprintf(format, args...))
}

// formatBytes muestra una cantidad de bytes en la unidad más legible.
func formatBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%dKB", n/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	}
}

// formatDuration muestra milisegundos por debajo de un segundo y segundos con
// un decimal por encima.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}

	return fmt.Sprintf("%.1fs", d.Seconds())
}
