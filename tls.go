package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// serveTLS serves h on HTTPS_PORT with a self-signed cert kept in ~/.stremio-lan.
func serveTLS(h http.Handler) {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".stremio-lan")
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if _, err := os.Stat(certFile); err != nil {
		if err := genCert(dir, certFile, keyFile); err != nil {
			log.Fatalf("tls cert: %v", err)
		}
	}
	addr := net.JoinHostPort(cfg.Bind, strconv.Itoa(cfg.HTTPSPort))
	log.Printf("listening on https://%s", addr)
	srv := newServer(h)
	srv.Addr = addr
	log.Fatal(srv.ListenAndServeTLS(certFile, keyFile))
}

func genCert(dir, certFile, keyFile string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "stremio-lan"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{host, "localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	if ip := net.ParseIP(cfg.Bind); ip != nil && !ip.IsUnspecified() {
		tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
	} else if addrs, err := net.InterfaceAddrs(); err == nil {
		// bound to all interfaces: cover every local IP
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() {
				tmpl.IPAddresses = append(tmpl.IPAddresses, n.IP)
			}
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}
