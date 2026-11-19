package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// No alternate runtime transport or handler is constructed here. Production
// passes the fully validated server and its own newly bound listener.
func serveIngress(ctx context.Context, server *http.Server, listener net.Listener) error {
	defer listener.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- server.ServeTLS(listener, "", "") }()
	select {
	case err := <-done:
		// Unexpected closure/startup errors must not become successful shutdown.
		return err
	case <-ctx.Done():
		// The signal context is already canceled; give actual in-flight HTTP
		// requests a separate finite drain budget, shorter than hosted Docker stop.
		drain, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return drainIngress(drain, server, done)
	}
}

func drainIngress(ctx context.Context, server *http.Server, done <-chan error) error {
	err := server.Shutdown(ctx)
	if err != nil {
		// Force-close on failed drain, but never report the failure as success.
		_ = server.Close()
	}
	serveErr := <-done
	if err != nil {
		return err
	}
	if !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}
