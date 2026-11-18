package managedgarm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Session owns stock administration credentials in the root supervisor only.
// It is NOT native capacity authority. It cannot increase pool capacity.
type Session struct {
	mu        sync.Mutex
	spec      Specification
	trust     *tls.Config
	namespace string
	record    bootstrapRecord
	token     string
	revoked   bool
}

// Bootstrap uses only actual observed topology and the prepared private root
// credential. Its durable attempt marker makes first-run at-most-once, including
// process restarts and lost responses. No containers are started here.
func Bootstrap(ctx context.Context, spec Specification) error {
	if os.Geteuid() != 0 {
		return ErrBoundary
	}
	files, e := openBootstrapFiles(spec.AdminSecretDirectory, 0)
	if e != nil {
		return ErrBoundary
	}
	defer files.root.Close()
	observed, e := Observe(ctx, spec)
	if e != nil {
		return ErrBoundary
	}
	trust, e := readStockTrust(spec)
	if e != nil {
		return ErrBoundary
	}
	s := &Session{spec: spec, namespace: observed.Namespace(), trust: trust}
	_, _, e = bootstrapProtocol(ctx, files, spec.GarmID, s.exchange)
	return e
}

// OpenSession never invokes first-run or adopts a missing bootstrap record.
// Authentication is renewed using the same generated private credential and the
// authenticated controller ID must still match the immutable bootstrap record.
func OpenSession(ctx context.Context, spec Specification) (*Session, error) {
	if os.Geteuid() != 0 {
		return nil, ErrBoundary
	}
	files, e := openBootstrapFiles(spec.AdminSecretDirectory, 0)
	if e != nil {
		return nil, ErrBoundary
	}
	defer files.root.Close()
	var before bootstrapRecord
	if files.read("bootstrap-record.json", &before) != nil || !stockUUID(before.ControllerID) {
		return nil, ErrBoundary
	}
	var attempt bootstrapAttempt
	if files.read("bootstrap-attempt.json", &attempt) != nil || before.bootstrapAttempt != attempt {
		return nil, ErrBoundary
	}
	observed, e := Observe(ctx, spec)
	if e != nil {
		return nil, ErrBoundary
	}
	trust, e := readStockTrust(spec)
	if e != nil {
		return nil, ErrBoundary
	}
	s := &Session{spec: spec, namespace: observed.Namespace(), trust: trust}
	record, token, e := bootstrapProtocol(ctx, files, spec.GarmID, s.exchange)
	if e != nil || record != before {
		return nil, ErrBoundary
	}
	s.record = record
	s.token = token
	return s, nil
}
func (s *Session) Close() { s.mu.Lock(); defer s.mu.Unlock(); s.revoked = true; s.token = "" }
func (s *Session) ControllerID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked {
		return ""
	}
	return s.record.ControllerID
}
func (s *Session) check(ctx context.Context) (Observation, error) {
	if s.revoked {
		return Observation{}, ErrBoundary
	}
	o, e := Observe(ctx, s.spec)
	if e != nil || o.Namespace() != s.namespace {
		s.revoked = true
		s.token = ""
		return Observation{}, ErrBoundary
	}
	return o, nil
}

// Namespace switching is confined to a dedicated locked OS thread. If restore
// fails, the goroutine exits without unlocking so Go destroys that thread rather
// than returning it to the scheduler in the wrong network namespace.
func dialNamespace(ctx context.Context, namespace *os.File) (net.Conn, error) {
	return dialNamespaceAddress(ctx, namespace, "127.0.0.1:9998")
}
func dialNamespaceAddress(ctx context.Context, namespace *os.File, address string) (net.Conn, error) {
	if address != "127.0.0.1:9998" && address != "127.0.0.1:9443" {
		return nil, ErrBoundary
	}

	type result struct {
		connection net.Conn
		err        error
	}
	done := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		original, e := os.Open("/proc/thread-self/ns/net")
		if e != nil {
			runtime.UnlockOSThread()
			done <- result{err: ErrBoundary}
			return
		}
		defer original.Close()
		if unix.Setns(int(namespace.Fd()), unix.CLONE_NEWNET) != nil {
			runtime.UnlockOSThread()
			done <- result{err: ErrBoundary}
			return
		}
		connection, e := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp4", address)
		if unix.Setns(int(original.Fd()), unix.CLONE_NEWNET) != nil {
			if connection != nil {
				connection.Close()
			}
			done <- result{err: ErrBoundary}
			return
		}
		runtime.UnlockOSThread()
		done <- result{connection, e}
	}()
	// Dial has the supplied deadline. Waiting for restoration avoids closing the
	// pinned namespace descriptor while the locked thread is still entering it.
	r := <-done
	return r.connection, r.err
}
func (s *Session) exchange(ctx context.Context, method, path, token string, body any) ([]byte, error) {
	o, e := s.check(ctx)
	if e != nil {
		return nil, ErrBoundary
	}
	namespace, e := o.OpenNetworkNamespace()
	if e != nil {
		s.revoked = true
		return nil, ErrBoundary
	}
	defer namespace.Close()
	tr := &http.Transport{Proxy: nil, TLSClientConfig: s.trust, DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dialNamespace(ctx, namespace) }, ResponseHeaderTimeout: 10 * time.Second}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrBoundary }}
	var raw []byte
	if body != nil {
		raw, e = json.Marshal(body)
		if e != nil {
			return nil, ErrBoundary
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, "https://127.0.0.1:9998"+path, bytes.NewReader(raw))
	if e != nil {
		return nil, ErrBoundary
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, e := client.Do(req)
	if e != nil {
		return nil, ErrBoundary
	}
	defer response.Body.Close()
	raw, e = io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if e != nil || len(raw) > 4<<20 {
		return nil, ErrBoundary
	}
	if _, e = s.check(ctx); e != nil {
		return nil, ErrBoundary
	}
	if response.StatusCode != 200 {
		s.revoked = true
		s.token = ""
		return nil, ErrBoundary
	}
	return raw, nil
}

// RoundTrip is used only by the scoped management adapter in the root process.
// No arbitrary upstream endpoint, login/token read or pass-through route exists.
// Full capacity increases require a future genuine native authority contract;
// this concrete implementation permits only max=0 or enabled=false pauses.
func (s *Session) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r == nil || r.URL == nil || r.URL.Scheme != "https" || r.URL.Host != "managed-garm.invalid" || r.URL.RawQuery != "" || r.URL.RawPath != "" || r.URL.Fragment != "" || r.URL.User != nil || s.token == "" || s.revoked {
		return nil, ErrBoundary
	}
	path := r.URL.Path
	prefix := "/api/v1/pools/"
	repoPrefix := "/api/v1/repositories/"
	allowedPool := len(path) > len(prefix) && path[:len(prefix)] == prefix && stockUUID(path[len(prefix):])
	allowedRepo := len(path) > len(repoPrefix) && path[:len(repoPrefix)] == repoPrefix && stockUUID(path[len(repoPrefix):])
	if !allowedPool && !allowedRepo {
		return nil, ErrBoundary
	}
	var body any
	if r.Method == http.MethodPut && allowedPool {
		if r.Body == nil {
			return nil, ErrBoundary
		}
		raw, e := io.ReadAll(io.LimitReader(r.Body, 4097))
		if e != nil || len(raw) > 4096 {
			return nil, ErrBoundary
		}
		var update struct {
			Maximum *uint64 `json:"max_runners,omitempty"`
			Enabled *bool   `json:"enabled,omitempty"`
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&update) != nil || d.Decode(new(any)) != io.EOF || (update.Maximum == nil && update.Enabled == nil) || (update.Maximum != nil && *update.Maximum != 0) || (update.Enabled != nil && *update.Enabled) {
			return nil, ErrBoundary
		}
		body = update
	} else if r.Method != http.MethodGet {
		return nil, ErrBoundary
	}
	raw, e := s.exchange(r.Context(), r.Method, path, s.token, body)
	if e != nil {
		return nil, e
	}
	return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw)), ContentLength: int64(len(raw)), Request: r}, nil
}

// Identity binds the existing settings journal to the authenticated stock
// controller and container, not a reusable URL or caller-selected source hash.
func (s *Session) Identity() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked || s.token == "" || !stockUUID(s.record.ControllerID) {
		return ""
	}
	sum := sha256.Sum256([]byte("managed-stock-settings/v1\x00" + s.spec.GarmID + "\x00" + s.record.ControllerID))
	return hex.EncodeToString(sum[:])
}
func (s *Session) Verify(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, e := s.check(ctx)
	return e
}

// Listen binds only the dedicated loopback proxy port in the currently observed
// namespace. The caller must wrap this listener with pinned mutual TLS.
func (s *Session) Listen(ctx context.Context) (net.Listener, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, e := s.check(ctx)
	if e != nil {
		return nil, e
	}
	namespace, e := o.OpenNetworkNamespace()
	if e != nil {
		return nil, ErrBoundary
	}
	defer namespace.Close()
	type result struct {
		listener net.Listener
		err      error
	}
	done := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		original, e := os.Open("/proc/thread-self/ns/net")
		if e != nil {
			runtime.UnlockOSThread()
			done <- result{err: ErrBoundary}
			return
		}
		defer original.Close()
		if unix.Setns(int(namespace.Fd()), unix.CLONE_NEWNET) != nil {
			runtime.UnlockOSThread()
			done <- result{err: ErrBoundary}
			return
		}
		listener, e := (&net.ListenConfig{}).Listen(ctx, "tcp4", "127.0.0.1:9445")
		if unix.Setns(int(original.Fd()), unix.CLONE_NEWNET) != nil {
			if listener != nil {
				listener.Close()
			}
			done <- result{err: ErrBoundary}
			return
		}
		runtime.UnlockOSThread()
		done <- result{listener, e}
	}()
	r := <-done
	if r.err != nil {
		return nil, ErrBoundary
	}
	if _, e = s.check(ctx); e != nil {
		r.listener.Close()
		return nil, e
	}
	return r.listener, nil
}

// ReadCollection is root-internal input to the scoped read facade. Its caller
// must project/restrict rows before returning anything to a controller. There is
// no arbitrary path, query, token/header forwarding or write capability here.
func (s *Session) ReadCollection(ctx context.Context, path string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked || s.token == "" {
		return nil, ErrBoundary
	}
	switch path {
	case "/api/v1/instances", "/api/v1/pools", "/api/v1/jobs":
	default:
		if !strings.HasPrefix(path, "/api/v1/pools/") || !stockUUID(strings.TrimPrefix(path, "/api/v1/pools/")) {
			return nil, ErrBoundary
		}
	}
	return s.exchange(ctx, http.MethodGet, path, s.token, nil)
}

func (s *Session) ReadInstances(ctx context.Context) ([]byte, error) {
	return s.ReadCollection(ctx, "/api/v1/instances")
}

// ValidateSupervisorFilePath rejects rename/symlink authority through writable
// ancestors. Callers must still use securefiles for owner/mode/no-follow checks
// on the leaf; this helper confers no runtime authority.
func ValidateSupervisorFilePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return ErrBoundary
	}
	return rootControlledAncestors(filepath.Dir(path))
}
