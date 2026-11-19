package managedwriter

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedgarm"
)

// Service owns the actual root writer listener/session lifetime while preserving
// the same durable settings journal across a controlled namespace transition.
// Lifecycle methods perform actions, not grant authority; a restart coordinator
// must independently reobserve the resulting source descriptor and topology.
type Service struct {
	options                        Options
	mu                             sync.Mutex
	boundary                       managedgarm.Specification
	journal                        *garmmanagement.Journal
	trust                          *tls.Config
	clientLeaf, inspectorLeaf      string
	providers, pools, repositories []string
	session                        *managedgarm.Session
	server                         *http.Server
	done                           chan error
	stopped                        bool
	closed                         bool
	terminal                       chan error
}

func NewService(ctx context.Context, boundary managedgarm.Specification, journal *garmmanagement.Journal, trust *tls.Config, clientLeaf, inspectorLeaf string, providers, pools, repositories []string) (*Service, error) {
	return NewServiceWithOptions(ctx, boundary, journal, trust, clientLeaf, inspectorLeaf, providers, pools, repositories, Options{})
}

func NewServiceWithOptions(ctx context.Context, boundary managedgarm.Specification, journal *garmmanagement.Journal, trust *tls.Config, clientLeaf, inspectorLeaf string, providers, pools, repositories []string, options Options) (*Service, error) {
	if ctx == nil || ctx.Err() != nil || journal == nil || trust == nil || trust.ClientAuth != tls.RequireAndVerifyClientCert || trust.ClientCAs == nil || len(trust.Certificates) != 1 || trust.InsecureSkipVerify || trust.VerifyConnection != nil || trust.VerifyPeerCertificate != nil || trust.GetCertificate != nil || trust.GetConfigForClient != nil || trust.KeyLogWriter != nil {
		return nil, managedgarm.ErrBoundary
	}
	if len(trust.Certificates[0].Certificate) == 0 || (trust.MaxVersion != 0 && trust.MaxVersion < tls.VersionTLS13) {
		return nil, managedgarm.ErrBoundary
	}
	serverHash := sha256.Sum256(trust.Certificates[0].Certificate[0])
	serverPin := hex.EncodeToString(serverHash[:])
	if clientLeaf == serverPin || clientLeaf == boundary.GarmServerFingerprint || (inspectorLeaf != "" && (inspectorLeaf == clientLeaf || inspectorLeaf == serverPin || inspectorLeaf == boundary.GarmServerFingerprint)) {
		return nil, managedgarm.ErrBoundary
	}
	trust = trust.Clone()
	if trust.MinVersion < tls.VersionTLS13 {
		trust.MinVersion = tls.VersionTLS13
	}
	s := &Service{options: options, boundary: boundary, journal: journal, trust: trust.Clone(), clientLeaf: clientLeaf, inspectorLeaf: inspectorLeaf, providers: append([]string(nil), providers...), pools: append([]string(nil), pools...), repositories: append([]string(nil), repositories...), stopped: true, terminal: make(chan error, 1)}
	if err := s.Resume(ctx); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Service) Resume(ctx context.Context) error {
	if s == nil || ctx == nil || ctx.Err() != nil {
		return managedgarm.ErrBoundary
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.stopped || s.server != nil || s.session != nil {
		return managedgarm.ErrBoundary
	}
	session, err := openWriterSession(ctx, s.boundary)
	if err != nil {
		return err
	}
	valid := false
	defer func() {
		if !valid {
			session.Close()
		}
	}()
	handler, err := NewWithOptions(session, s.journal, s.clientLeaf, s.providers, s.pools, s.repositories, s.options)
	if err != nil {
		return err
	}
	if s.inspectorLeaf != "" {
		if err := handler.SetSourceInspector(s.inspectorLeaf); err != nil {
			return err
		}
	}
	listener, err := session.Listen(ctx)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 16384}
	done := make(chan error, 1)
	s.session = session
	s.server = server
	s.done = done
	s.stopped = false
	valid = true
	go func() {
		err := server.Serve(tls.NewListener(listener, s.trust))
		listener.Close()
		done <- err
		close(done)
		if err != http.ErrServerClosed {
			select {
			case s.terminal <- err:
			default:
			}
		}
	}()
	return nil
}
func (s *Service) Suspend(ctx context.Context) error {
	if s == nil || ctx == nil {
		return managedgarm.ErrBoundary
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.server == nil || s.session == nil {
		return managedgarm.ErrBoundary
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Shutdown closes the listener first and waits for actual in-flight handlers.
	// On timeout we refuse the container restart rather than assume writes ended.
	if err := s.server.Shutdown(bounded); err != nil {
		return managedgarm.ErrBoundary
	}
	select {
	case <-s.done:
	case <-bounded.Done():
		return managedgarm.ErrBoundary
	}
	s.session.Close()
	s.server = nil
	s.session = nil
	s.stopped = true
	return nil
}
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		s.server.Close()
	}
	if s.session != nil {
		s.session.Close()
	}
	s.server = nil
	s.session = nil
	s.stopped = true
	s.closed = true
	select {
	case s.terminal <- nil:
	default:
	}
}
func (s *Service) Wait(ctx context.Context) error {
	if s == nil || ctx == nil {
		return managedgarm.ErrBoundary
	}
	s.mu.Lock()
	terminal := s.terminal
	s.mu.Unlock()
	if terminal == nil {
		return managedgarm.ErrBoundary
	}
	select {
	case <-ctx.Done():
		s.Close()
		return ctx.Err()
	case err := <-terminal:
		return err
	}
}

func openWriterSession(ctx context.Context, boundary managedgarm.Specification) (*managedgarm.Session, error) {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		session, err := managedgarm.OpenSession(bounded, boundary)
		if err == nil {
			return session, nil
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-bounded.Done():
			timer.Stop()
			return nil, managedgarm.ErrBoundary
		case <-timer.C:
		}
	}
}
