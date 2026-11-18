package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockingress"
)

type config struct{ Listen, Upstream, Controller, TLSCertificate, TLSKey, StockCertificate, StockFingerprint string }

func run(args []string) error {
	if len(args) != 1 {
		return stockingress.ErrConfiguration
	}
	raw, e := securefiles.Read(args[0], 16384)
	if e != nil {
		return stockingress.ErrConfiguration
	}
	var c config
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return stockingress.ErrConfiguration
	}
	host, port, e := net.SplitHostPort(c.Listen)
	if e != nil || net.ParseIP(host) == nil || port == "" {
		return stockingress.ErrConfiguration
	}
	stockPEM, e := securefiles.Read(c.StockCertificate, 1<<20)
	if e != nil {
		return stockingress.ErrConfiguration
	}
	block, rest := pem.Decode(stockPEM)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return stockingress.ErrConfiguration
	}
	sum := sha256.Sum256(block.Bytes)
	if c.StockFingerprint != hex.EncodeToString(sum[:]) {
		return stockingress.ErrConfiguration
	}
	stockCert, e := x509.ParseCertificate(block.Bytes)
	if e != nil {
		return stockingress.ErrConfiguration
	}
	roots := x509.NewCertPool()
	roots.AddCert(stockCert)
	trust := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "127.0.0.1"}
	handler, e := stockingress.NewTLS(c.Upstream, c.Controller, trust)
	if e != nil {
		return e
	}
	certPEM, e := securefiles.Read(c.TLSCertificate, 1<<20)
	if e != nil {
		return stockingress.ErrConfiguration
	}
	keyPEM, e := securefiles.Read(c.TLSKey, 1<<20)
	if e != nil {
		return stockingress.ErrConfiguration
	}
	pair, e := tls.X509KeyPair(certPEM, keyPEM)
	if e != nil {
		return stockingress.ErrConfiguration
	}
	server := &http.Server{Addr: c.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 60 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}}
	return server.ListenAndServeTLS("", "")
}
func main() {
	if run(os.Args[1:]) != nil {
		fmt.Fprintln(os.Stderr, "stock ingress unavailable")
		os.Exit(1)
	}
}
