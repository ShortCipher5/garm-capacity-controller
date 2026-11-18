package managedwriter

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/managedgarm"
)

func TestWriterSuspendWaitsForActualHandlersAndDoesNotTerminateOwner(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; w.WriteHeader(200) })}
	done := make(chan error, 1)
	s := &Service{server: server, session: &managedgarm.Session{}, done: done, terminal: make(chan error, 1)}
	defer s.Close()
	go func() { done <- server.Serve(listener); close(done) }()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		response, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			response.Body.Close()
		}
	}()
	<-entered
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if s.Suspend(short) == nil {
		t.Fatal("active request treated as drained")
	}
	if s.stopped || s.session == nil {
		t.Fatal("unconfirmed suspension cleared live state")
	}
	close(release)
	<-requestDone
	if err := s.Suspend(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s.stopped || s.session != nil || s.server != nil {
		t.Fatal("suspension did not clear old listener/session")
	}
	select {
	case <-s.terminal:
		t.Fatal("suspending terminated service owner")
	default:
	}
	s.Close()
	if err := s.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Resume(context.Background()) == nil {
		t.Fatal("closed service resumed")
	}
}
func TestWriterLifecycleCannotBeConstructedFromMissingAuthority(t *testing.T) {
	if _, err := NewService(context.Background(), managedgarm.Specification{}, nil, nil, "", "", nil, nil, nil); err == nil {
		t.Fatal("missing real session config accepted")
	}
	var service *Service
	if service.Resume(context.Background()) == nil || service.Suspend(context.Background()) == nil || service.Wait(context.Background()) == nil {
		t.Fatal("nil service accepted")
	}
}
