package main

import (
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

func TestNewProxyDefaults(t *testing.T) {
	log := NewLogger(io.Discard)
	p := NewProxy(log)
	if p.Log != log {
		t.Errorf("Log isn't the logger passed in")
	}
	if p.DialTimeout != 10*time.Second {
		t.Errorf("DialTimeout = %v, want 10s", p.DialTimeout)
	}
	if p.IdleTimeout != 60*time.Second {
		t.Errorf("IdleTimeout = %v, want 60s", p.IdleTimeout)
	}
	if p.Verbose {
		t.Errorf("Verbose should start as false")
	}
	if p.Dial != nil {
		t.Errorf("Dial should start as nil")
	}
}

func TestStatusForDialError(t *testing.T) {
	timeout := &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	dns := &net.DNSError{Err: "no such host", Name: "nothere.invalid", IsNotFound: true}
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"timeout", timeout, 504},
		{"dns", dns, 502},
		{"connection refused", errors.New("connection refused"), 502},
	}
	for _, c := range cases {
		if got := statusForDialError(c.err); got != c.want {
			t.Errorf("%s: statusForDialError = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestDialUsesInjectedFunc(t *testing.T) {
	sentinel := errors.New("test dial")
	p := NewProxy(NewLogger(io.Discard))
	p.Dial = func(addr string) (net.Conn, error) { return nil, sentinel }
	if _, err := p.dial("x:1"); !errors.Is(err, sentinel) {
		t.Fatalf("dial returned %v, want the injected function's error", err)
	}
}

func TestDialDefault(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	p := NewProxy(NewLogger(io.Discard))
	conn, err := p.dial(ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.Close()
}
