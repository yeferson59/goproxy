package main

import (
	"bytes"
	"errors"
	"net/http"
	"testing"
	"time"
)

// newTestLogger fixes the time so lines can be compared.
func newTestLogger() (*Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	l := NewLogger(&buf)
	l.now = func() time.Time { return time.Date(2026, 11, 23, 14, 2, 11, 0, time.Local) }
	return l, &buf
}

func TestFormatBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0B", 512: "512B", 49152: "48KB", 1258291: "1.2MB"} {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                       "0ms",
		12 * time.Millisecond:   "12ms",
		999 * time.Millisecond:  "999ms",
		3100 * time.Millisecond: "3.1s",
	}
	for d, want := range cases {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestLoggerLines(t *testing.T) {
	cases := []struct {
		name string
		log  func(*Logger)
		want string
	}{
		{"http", func(l *Logger) { l.HTTP("GET", "http://example.com/", 200, 12*time.Millisecond) },
			"14:02:11 GET     http://example.com/       200  12ms\n"},
		{"http error", func(l *Logger) {
			l.HTTPError("GET", "http://nothere.invalid/", 502, errors.New("dial: no such host"))
		},
			"14:02:11 GET     http://nothere.invalid/   502  dial: no such host\n"},
		{"tunnel open", func(l *Logger) { l.TunnelOpen("www.google.com:443") },
			"14:02:11 CONNECT www.google.com:443        tunnel open\n"},
		{"tunnel closed", func(l *Logger) {
			l.TunnelClosed("www.google.com:443", 49152, 1258291, 3100*time.Millisecond)
		},
			"14:02:11 CONNECT www.google.com:443        closed  48KB↑ 1.2MB↓  3.1s\n"},
		{"error", func(l *Logger) { l.Errorf("malformed request from %s", "127.0.0.1:5000") },
			"14:02:11 ERROR   malformed request from 127.0.0.1:5000\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, buf := newTestLogger()
			c.log(l)
			if got := buf.String(); got != c.want {
				t.Errorf("\ngot  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestLoggerHeaders(t *testing.T) {
	l, buf := newTestLogger()
	l.Headers("request from client", http.Header{
		"User-Agent": {"curl/8.0"},
		"Accept":     {"*/*"},
		"X-Multi":    {"a", "b"},
	})
	want := "    --- request from client ---\n" +
		"    Accept: */*\n" +
		"    User-Agent: curl/8.0\n" +
		"    X-Multi: a\n" +
		"    X-Multi: b\n"
	if got := buf.String(); got != want {
		t.Errorf("\ngot  %q\nwant %q", got, want)
	}
}
