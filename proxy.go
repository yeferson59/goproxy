package main

import (
	"errors"
	"net"
	"net/http"
	"time"
)

// Proxy holds the configuration shared by every connection.
type Proxy struct {
	// Log receives one line per request or tunnel.
	Log *Logger
	// Verbose turns on the header dump at each hop (-v).
	Verbose bool
	// DialTimeout limits how long dialing the target may take.
	DialTimeout time.Duration
	// IdleTimeout closes the connection to a client that doesn't send a new
	// request within that time.
	IdleTimeout time.Duration
	// Dial replaces the connection to the target. When nil, real TCP is
	// used. Tests swap it to simulate failures that are hard to trigger,
	// such as a timeout.
	Dial func(addr string) (net.Conn, error)
}

// NewProxy creates a Proxy with the default timeouts: 10 s to dial the
// target and 60 s of client inactivity between requests.
func NewProxy(log *Logger) *Proxy {
	return new(Proxy{
		Log:         log,
		DialTimeout: 10 * time.Second,
		IdleTimeout: 60 * time.Second,
	})
}

// dial opens the TCP connection to the target addr ("host:port").
func (p *Proxy) dial(addr string) (net.Conn, error) {
	if p.Dial != nil {
		return p.Dial(addr)
	}

	return net.DialTimeout("tcp", addr, p.DialTimeout)
}

// statusForDialError maps a failure to reach the target to the status code
// the client sees: 504 if the dial timed out, 502 for everything else (DNS,
// connection refused, network unreachable).
func statusForDialError(err error) int {
	// net.Error is the interface Go's network errors implement.
	// Timeout() tells "the target didn't answer in time" apart from "the
	// target said no" or "the name doesn't exist".
	var netErr net.Error

	if errors.As(err, &netErr) && netErr.Timeout() {
		return http.StatusGatewayTimeout
	}

	return http.StatusBadGateway
}
