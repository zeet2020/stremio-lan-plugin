package main

import (
	"net"
	"testing"
)

func TestShareHost(t *testing.T) {
	cfg = Config{Bind: "192.168.0.101"} // pins lanIP() so the test doesn't depend on this machine
	for in, want := range map[string]string{
		"127.0.0.1:7070":    "192.168.0.101:7070",
		"localhost:7070":    "192.168.0.101:7070",
		"[::1]:7070":        "192.168.0.101:7070",
		"localhost":         "192.168.0.101",
		"192.168.0.50:7070": "192.168.0.50:7070", // already a LAN address: keep it
		"nas.lan:7070":      "nas.lan:7070",
	} {
		if got := shareHost(in); got != want {
			t.Errorf("shareHost(%q) = %q, want %q", in, got, want)
		}
	}
	cfg = Config{Bind: "0.0.0.0"} // real detection: must not hand out a loopback address
	if h, _, _ := net.SplitHostPort(shareHost("127.0.0.1:7070")); net.ParseIP(h) != nil && net.ParseIP(h).IsLoopback() && lanIP() != "" {
		t.Errorf("still loopback with a LAN IP available (%s)", lanIP())
	}
}
