package main

import (
	"net/http"
	"testing"
)

func TestRemoveHopByHop(t *testing.T) {
	h := http.Header{}
	// Connection nombra X-Secret: aunque no esté en la lista fija, también es
	// de salto para este mensaje.
	h.Set("Connection", "X-Secret, Keep-Alive")
	for _, k := range []string{"X-Secret", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Set(k, "x")
	}
	h.Set("X-Normal", "ok")

	removeHopByHop(h)

	if len(h) != 1 || h.Get("X-Normal") != "ok" {
		t.Errorf("solo debería quedar X-Normal; quedó %v", h)
	}
}

func TestRemoveHopByHopMultipleConnectionHeaders(t *testing.T) {
	// Connection puede venir repetida, y cada valor puede traer varios
	// nombres separados por coma.
	h := http.Header{
		"Connection": {"A", "B, C"},
		"A":          {"1"},
		"B":          {"2"},
		"C":          {"3"},
		"X-Normal":   {"ok"},
	}

	removeHopByHop(h)

	if len(h) != 1 || h.Get("X-Normal") != "ok" {
		t.Errorf("solo debería quedar X-Normal; quedó %v", h)
	}
}
