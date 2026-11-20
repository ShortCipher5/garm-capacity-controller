package managedgarm

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// This provisional review permits only an explicitly invoked fresh-pilot
// bootstrap operation. It cannot satisfy the completed read-session binding.
type stockPilotBootstrapReview struct{ Version, GarmID, LeafSHA256, CredentialDigest string }
type stockPilotBootstrapStore struct {
	root      *os.Root
	path      string
	directory os.FileInfo
	review    stockPilotBootstrapReview
	trust     *tls.Config
	ctx       context.Context
	poisoned  bool
}

func validateStockPilotBootstrap(garmID string, raw [3][]byte) (stockPilotBootstrapReview, *tls.Config, error) {
	var review stockPilotBootstrapReview
	var user credential
	if !stockBindingJSON(raw[0], &review) || review.Version != "stock-pilot-bootstrap/v1" || review.GarmID != garmID || !digest.MatchString(garmID) || !digest.MatchString(review.LeafSHA256) || !digest.MatchString(review.CredentialDigest) || !stockBindingJSON(raw[2], &user) || !validBootstrapCredential(user) || credentialHash(user) != review.CredentialDigest {
		return stockPilotBootstrapReview{}, nil, ErrBoundary
	}
	cert, err := x509.ParseCertificate(raw[1])
	if err != nil || cert.IsCA || cert.VerifyHostname("127.0.0.1") != nil {
		return stockPilotBootstrapReview{}, nil, ErrBoundary
	}
	serverAuth := false
	for _, usage := range cert.ExtKeyUsage {
		if usage == x509.ExtKeyUsageServerAuth {
			serverAuth = true
		}
	}
	if !serverAuth {
		return stockPilotBootstrapReview{}, nil, ErrBoundary
	}
	trust, err := stockTrust(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw[1]}), review.LeafSHA256)
	if err != nil {
		return stockPilotBootstrapReview{}, nil, ErrBoundary
	}
	return review, trust, nil
}
func openStockPilotBootstrapStore(ctx context.Context, p stockReadProfile) (*stockPilotBootstrapStore, error) {
	if ctx == nil || ctx.Err() != nil || os.Geteuid() != 0 || !validStockReadProfile(p) {
		return nil, ErrBoundary
	}
	path := filepath.Join(p.Directory, "supervisor-admin")
	if rootControlledAncestors(path) != nil {
		return nil, ErrBoundary
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0700 {
		return nil, ErrBoundary
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 {
		return nil, ErrBoundary
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrBoundary
	}
	s := &stockPilotBootstrapStore{root: root, path: path, directory: fi, ctx: ctx}
	review, trust, err := s.material(p.IDs[0])
	if err != nil {
		root.Close()
		return nil, ErrBoundary
	}
	s.review = review
	s.trust = trust
	return s, nil
}
func (s *stockPilotBootstrapStore) material(id string) (stockPilotBootstrapReview, *tls.Config, error) {
	fail := func() (stockPilotBootstrapReview, *tls.Config, error) {
		return stockPilotBootstrapReview{}, nil, ErrBoundary
	}
	if s == nil || s.root == nil || s.ctx == nil || s.ctx.Err() != nil || s.poisoned {
		return fail()
	}
	names := [3]string{"stock-bootstrap-binding.json", "stock-api.der", "bootstrap-user.json"}
	var raw [3][]byte
	defer func() {
		for _, b := range raw {
			clear(b)
		}
	}()
	for i, name := range names {
		var err error
		raw[i], err = stockReadAuthFile(s.root, name)
		if err != nil {
			return fail()
		}
	}
	review, trust, err := validateStockPilotBootstrap(id, raw)
	if err != nil {
		return fail()
	}
	for i, name := range names {
		again, e := stockReadAuthFile(s.root, name)
		same := e == nil && bytes.Equal(again, raw[i])
		clear(again)
		if !same {
			return fail()
		}
	}
	fi, e := os.Lstat(s.path)
	held, he := s.root.Stat(".")
	if e != nil || he != nil || !os.SameFile(fi, s.directory) || !os.SameFile(held, s.directory) || !fi.IsDir() || fi.Mode().Perm() != 0700 || rootControlledAncestors(s.path) != nil || s.ctx.Err() != nil {
		return fail()
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 {
		return fail()
	}
	return review, trust, nil
}
func (s *stockPilotBootstrapStore) check() error {
	review, _, err := s.material(s.review.GarmID)
	if err != nil || review != s.review {
		s.poisoned = true
		return ErrBoundary
	}
	return nil
}
func (s *stockPilotBootstrapStore) read(name string, out any) error {
	if name != "bootstrap-user.json" && name != "bootstrap-attempt.json" && name != "bootstrap-record.json" {
		return ErrBoundary
	}
	if s.check() != nil {
		return ErrBoundary
	}
	if _, err := s.root.Lstat(name); errors.Is(err, os.ErrNotExist) {
		return os.ErrNotExist
	} else if err != nil {
		return ErrBoundary
	}
	raw, err := stockReadAuthFile(s.root, name)
	defer clear(raw)
	if err != nil || !stockBindingJSON(raw, out) || s.check() != nil {
		return ErrBoundary
	}
	return nil
}
func (s *stockPilotBootstrapStore) create(name string, value any) error {
	if s.check() != nil {
		return ErrBoundary
	}
	wanted := bootstrapAttempt{"managed-garm-bootstrap/v1", s.review.GarmID, s.review.CredentialDigest}
	switch name {
	case "bootstrap-attempt.json":
		v, ok := value.(bootstrapAttempt)
		if !ok || v != wanted {
			return ErrBoundary
		}
	case "bootstrap-record.json":
		v, ok := value.(bootstrapRecord)
		if !ok || v.bootstrapAttempt != wanted || !stockUUID(v.ControllerID) || v.UserID != "" {
			return ErrBoundary
		}
	default:
		return ErrBoundary
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ErrBoundary
	}
	file, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return ErrBoundary
	}
	// Any uncertainty after exclusive creation is permanent for this invocation;
	// the durable partial file also prevents another first-run after reopening.
	s.poisoned = true
	err = file.Chmod(0600)
	if err == nil {
		var n int
		n, err = file.Write(raw)
		if err == nil && n != len(raw) {
			err = io.ErrShortWrite
		}
	}
	if err == nil {
		err = file.Sync()
	}
	ce := file.Close()
	if err != nil || ce != nil {
		return ErrBoundary
	}
	dir, err := s.root.Open(".")
	if err != nil {
		return ErrBoundary
	}
	err = dir.Sync()
	ce = dir.Close()
	if err != nil || ce != nil {
		return ErrBoundary
	}
	s.poisoned = false
	return s.check()
}

// Explicit, currently uncalled pilot-only setup. No listener, read facade,
// admission object, credential creation, directory creation or completed review
// is produced. Existing deployments/writer/restart never call this function.
func bootstrapStockReadPilot(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrBoundary
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	held, err := retainStockReadOnly(ctx)
	if err != nil {
		return ErrBoundary
	}
	defer held.close()
	store, err := openStockPilotBootstrapStore(ctx, held.observed.profile)
	if err != nil {
		return ErrBoundary
	}
	defer store.root.Close()
	// Exclusive attempt creation is the at-most-once first-run linearization.
	// Concurrent explicit recovery may fail but cannot create a second attempt.
	_, _, err = bootstrapProtocol(ctx, store, store.review.GarmID, func(ctx context.Context, method, path, token string, body any) ([]byte, error) {
		return stockPilotBootstrapExchange(ctx, held, store, method, path, token, body)
	})
	if err != nil || store.check() != nil || held.recheck(ctx) != nil {
		return ErrBoundary
	}
	return nil
}
func stockPilotBootstrapRoute(method, path, token string, body any) bool {
	return (method == http.MethodPost && (path == "/api/v1/first-run" || path == "/api/v1/auth/login") && token == "" && body != nil) || (method == http.MethodGet && path == "/api/v1/controller" && token != "" && body == nil)
}
func stockPilotBootstrapExchange(ctx context.Context, held *retainedStockRead, store *stockPilotBootstrapStore, method, path, token string, body any) ([]byte, error) {
	if ctx == nil || ctx.Err() != nil || !stockPilotBootstrapRoute(method, path, token, body) || held == nil || store == nil || store.check() != nil || held.recheck(ctx) != nil {
		return nil, ErrBoundary
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tr := &http.Transport{Proxy: nil, TLSClientConfig: store.trust, DisableKeepAlives: true, ResponseHeaderTimeout: 10 * time.Second, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "127.0.0.1:9998" {
			return nil, ErrBoundary
		}
		return held.dialStockAPI(ctx)
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrBoundary }}
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return nil, ErrBoundary
		}
		defer clear(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://127.0.0.1:9998"+path, bytes.NewReader(raw))
	if err != nil {
		return nil, ErrBoundary
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, ErrBoundary
	}
	defer response.Body.Close()
	result, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(result) > 4<<20 || response.StatusCode != http.StatusOK || store.check() != nil || held.recheck(ctx) != nil || ctx.Err() != nil {
		clear(result)
		return nil, ErrBoundary
	}
	return result, nil
}
