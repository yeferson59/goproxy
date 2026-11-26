package main

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Logger writes one line per proxy event.
//
// Each connection runs in its own goroutine, so several can log at once: the
// mutex keeps two lines from getting mixed halfway.
type Logger struct {
	mu  sync.Mutex
	out io.Writer
	// now can be replaced in tests to fix the time.
	now func() time.Time
}

// NewLogger creates a Logger that writes to out.
func NewLogger(out io.Writer) *Logger {
	return &Logger{out: out, now: time.Now}
}

// line writes "time METHOD target rest" in aligned columns.
func (l *Logger) line(method, target, rest string) {
	l.mu.Lock()

	defer l.mu.Unlock()

	fmt.Fprintf(l.out, "%s %-7s %-25s %s\n", l.now().Format("15:04:05"), method, target, rest)
}

// HTTP logs a successfully forwarded request.
func (l *Logger) HTTP(method, target string, status int, elapsed time.Duration) {
	l.line(method, target, fmt.Sprintf("%d  %s", status, formatDuration(elapsed)))
}

// HTTPError logs a request that ended in an error response from the proxy
// itself (400, 502, 504).
func (l *Logger) HTTPError(method, target string, status int, err error) {
	l.line(method, target, fmt.Sprintf("%d  %v", status, err))
}

// TunnelOpen logs that a CONNECT tunnel was established.
func (l *Logger) TunnelOpen(target string) {
	l.line("CONNECT", target, "túnel abierto")
}

// TunnelClosed logs a tunnel closing: bytes sent up (client → target),
// bytes received down (target → client) and how long it lasted.
func (l *Logger) TunnelClosed(target string, up, down int64, elapsed time.Duration) {
	l.line("CONNECT", target, fmt.Sprintf("cerrado  %s↑ %s↓  %s", formatBytes(up), formatBytes(down), formatDuration(elapsed)))
}

// Headers dumps the headers under a title, one line per value. It's used
// with -v to see what changes at each hop.
func (l *Logger) Headers(title string, h http.Header) {
	// A Go map has no order: sorting the keys makes two dumps of the same
	// headers look identical, so they can be compared by eye.
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

// Errorf logs an error that isn't tied to a specific request.
func (l *Logger) Errorf(format string, args ...any) {
	l.mu.Lock()

	defer l.mu.Unlock()

	fmt.Fprintf(l.out, "%s %-7s %s\n", l.now().Format("15:04:05"), "ERROR", fmt.Sprintf(format, args...))
}

// formatBytes shows a byte count in the most readable unit.
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

// formatDuration shows milliseconds below one second, and seconds with one
// decimal above.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}

	return fmt.Sprintf("%.1fs", d.Seconds())
}
