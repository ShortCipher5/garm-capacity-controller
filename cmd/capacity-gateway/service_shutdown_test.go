package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func awaitShutdownEvent(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("actual shutdown event did not settle")
	}
}

// Actual loopback HTTP requests remain in accepted handlers even after forced
// socket close. This qualifies request/resource ordering, not runner drain.
func TestServiceShutdownJoinsBothAcceptedListeners(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "graceful", true: "deadline-is-not-join"}[force], func(t *testing.T) {
			g := newServiceIngress()
			started := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
			release := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
			closed := [2]sync.Once{}
			allow := func(i int) { closed[i].Do(func() { close(release[i]) }) }
			shutting := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
			clientsDone := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
			servers := [2]*httptest.Server{}
			for i := range servers {
				i := i
				servers[i] = httptest.NewServer(g.handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					close(started[i])
					// Deliberately ignore cancellation to demonstrate that closing
					// the socket alone is not accepted-work completion.
					<-release[i]
					w.WriteHeader(http.StatusOK)
				})))
				servers[i].Config.RegisterOnShutdown(func() { close(shutting[i]) })
				go func() {
					defer close(clientsDone[i])
					response, err := servers[i].Client().Get(servers[i].URL)
					if err == nil {
						_, _ = io.Copy(io.Discard, response.Body)
						_ = response.Body.Close()
					}
				}()
			}
			t.Cleanup(func() {
				allow(0)
				allow(1)
				servers[0].Close()
				servers[1].Close()
			})
			awaitShutdownEvent(t, started[0])
			awaitShutdownEvent(t, started[1])
			timeout := 2 * time.Second
			if force {
				timeout = 20 * time.Millisecond
			}
			result := make(chan error, 1)
			go func() { result <- stopAndJoinService(g, timeout, servers[0].Config, servers[1].Config) }()
			// BOTH drains start without waiting for the first blocked handler.
			awaitShutdownEvent(t, shutting[0])
			awaitShutdownEvent(t, shutting[1])
			called := false
			late := httptest.NewRecorder()
			g.handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(late, httptest.NewRequest(http.MethodGet, "/", nil))
			if called || late.Code != http.StatusServiceUnavailable || late.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("new work entered after the shared latch")
			}
			if force {
				// Closing the actual client sockets is observable independently
				// of the deliberately still-blocked server handlers.
				awaitShutdownEvent(t, clientsDone[0])
				awaitShutdownEvent(t, clientsDone[1])
			}
			select {
			case <-result:
				t.Fatal("shutdown returned before either accepted handler settled")
			default:
			}
			allow(0)
			select {
			case <-result:
				t.Fatal("first listener completion hid second accepted handler")
			default:
			}
			allow(1)
			select {
			case err := <-result:
				if force != errors.Is(err, errServiceShutdown) || (!force && err != nil) {
					t.Fatal("graceful/uncertain outcome changed", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("joined shutdown did not finish")
			}
			awaitShutdownEvent(t, clientsDone[0])
			awaitShutdownEvent(t, clientsDone[1])
		})
	}
}

func TestServiceIngressPanicStillSettlesAcceptedCount(t *testing.T) {
	g := newServiceIngress()
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("fixture panic unexpectedly hidden")
			}
		}()
		g.handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("synthetic handler failure") })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}()
	joined := make(chan error, 1)
	go func() { joined <- stopAndJoinService(g, time.Second) }()
	select {
	case err := <-joined:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("panic leaked accepted-work count")
	}
}
