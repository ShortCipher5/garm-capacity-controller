// Synthetic loopback HTTPS witness only, never a Garm/gateway/provider service.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	certificate, e := tls.LoadX509KeyPair("/run/anchor-private/server.pem", "/run/anchor-private/server.key")
	if e != nil {
		os.Exit(1)
	}
	witness, e := os.ReadFile("/run/anchor-private/witness")
	if e != nil || len(witness) < 16 || len(witness) > 128 {
		os.Exit(1)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(witness)
	})
	server := &http.Server{Addr: "127.0.0.1:9444", Handler: handler, TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13}, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = server.Shutdown(shutdown)
		close(done)
	}()
	if e = server.ListenAndServeTLS("", ""); !errors.Is(e, http.ErrServerClosed) {
		os.Exit(1)
	}
	<-done
}
