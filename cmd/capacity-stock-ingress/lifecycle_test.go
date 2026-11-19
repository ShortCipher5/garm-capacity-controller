package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/stockingress"
)

func ingressLifecycleFixture(t *testing.T, handler http.Handler) (*http.Server, net.Listener, *http.Client, string) {
	t.Helper()
	upstream := httptest.NewTLSServer(handler)
	t.Cleanup(upstream.Close)
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	proxy, err := stockingress.NewTLS(upstream.URL, "", &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	server := &http.Server{Handler: proxy, TLSConfig: &tls.Config{Certificates: upstream.TLS.Certificates, MinVersion: tls.VersionTLS12}}
	t.Cleanup(func() { server.Close() })
	client := upstream.Client()
	client.Timeout = 5 * time.Second
	t.Cleanup(client.CloseIdleConnections)
	return server, listener, client, "https://" + listener.Addr().String() + "/api/v1/metadata/system/service-name"
}

func TestIngressCancellationDrainsActualTLSRequest(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	server, listener, client, url := ingressLifecycleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		io.WriteString(w, "synthetic-read")
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveIngress(ctx, server, listener) }()
	response := make(chan error, 1)
	go func() {
		res, err := client.Get(url)
		if err == nil {
			defer res.Body.Close()
			data, e := io.ReadAll(res.Body)
			err = e
			if err == nil && (res.StatusCode != 200 || string(data) != "synthetic-read") {
				err = errors.New("changed read response")
			}
		}
		response <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not enter")
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("returned before drain: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	// Release once while retaining deferred cleanup if an assertion failed first.
	release <- struct{}{}
	select {
	case err := <-response:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("response not completed")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown not joined")
	}
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("listener survived shutdown")
	}
}

func TestIngressFailedDrainNeverReturnsSuccess(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	server, listener, client, url := ingressLifecycleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release }))
	done := make(chan error, 1)
	go func() { done <- server.ServeTLS(listener, "", "") }()
	response := make(chan error, 1)
	go func() {
		res, err := client.Get(url)
		if res != nil {
			res.Body.Close()
		}
		response <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not enter")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := drainIngress(ctx, server, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("failed drain returned %v", err)
	}
	select {
	case err := <-response:
		if err == nil {
			t.Fatal("failed drain returned completed response")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("force close did not finish client")
	}
	release <- struct{}{}
}

func TestIngressCanceledStartupAndServeFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runContext(ctx, []string{"/absent/private-config"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	server, listener, _, _ := ingressLifecycleFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") }))
	if err := serveIngress(ctx, server, listener); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// A closed listener is a real startup failure, not successful signal drain.
	if err := serveIngress(context.Background(), server, listener); err == nil {
		t.Fatal("closed listener accepted")
	}
}
