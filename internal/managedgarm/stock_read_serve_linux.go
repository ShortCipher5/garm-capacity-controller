package managedgarm

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/managedread"
)

// Fixed, root-reviewed role commitments. No endpoint, scope or callback input.
type stockReadServeReview struct {
	Version, GarmID, ControllerID, CASHA256, ServerSHA256, ClientSHA256 string
	GatewaySPKI, IngressSPKI                                            string
}
type stockReadServeMaterial struct {
	review     stockReadServeReview
	tls        *tls.Config
	commitment string
}

func validateStockReadServeMaterial(raw [5][]byte, auth stockReadAuthBinding, stockDER []byte) (stockReadServeMaterial, error) {
	fail := func() (stockReadServeMaterial, error) { return stockReadServeMaterial{}, ErrBoundary }
	var r stockReadServeReview
	if !stockBindingJSON(raw[0], &r) || r.Version != "stock-read-serve/v1" || r.GarmID != auth.review.GarmID || r.ControllerID != auth.record.ControllerID || !digest.MatchString(r.GarmID) || !stockUUID(r.ControllerID) {
		return fail()
	}
	ca, e := x509.ParseCertificate(bytes.Clone(raw[1]))
	if e != nil {
		return fail()
	}
	pair, e := tls.X509KeyPair(raw[2], raw[3])
	if e != nil || len(pair.Certificate) != 1 {
		return fail()
	}
	server, e := x509.ParseCertificate(pair.Certificate[0])
	if e != nil {
		return fail()
	}
	client, e := x509.ParseCertificate(raw[4])
	if e != nil {
		return fail()
	}
	stock, e := x509.ParseCertificate(stockDER)
	if e != nil || stockBindingDigest(stockDER) != auth.review.LeafSHA256 {
		return fail()
	}
	if stockBindingDigest(raw[1]) != r.CASHA256 || stockBindingDigest(server.Raw) != r.ServerSHA256 || stockBindingDigest(raw[4]) != r.ClientSHA256 || !ca.IsCA || server.IsCA || client.IsCA {
		return fail()
	}
	if !digest.MatchString(r.GatewaySPKI) || !digest.MatchString(r.IngressSPKI) || r.GatewaySPKI == r.IngressSPKI {
		return fail()
	}
	keys := map[string]bool{r.GatewaySPKI: true, r.IngressSPKI: true}
	for _, cert := range []*x509.Certificate{ca, server, client, stock} {
		key := stockBindingDigest(cert.RawSubjectPublicKeyInfo)
		if keys[key] {
			return fail()
		}
		keys[key] = true
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if ca.CheckSignatureFrom(ca) != nil {
		return fail()
	}
	now := time.Now()
	if now.Before(ca.NotBefore) || !now.Before(ca.NotAfter) {
		return fail()
	}
	if _, e = server.Verify(x509.VerifyOptions{Roots: roots, DNSName: "127.0.0.1", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, CurrentTime: now}); e != nil {
		return fail()
	}
	if _, e = client.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, CurrentTime: now}); e != nil {
		return fail()
	}
	// Exact role leaf remains mandatory in addition to normal chain verification.
	pin := r.ClientSHA256
	config := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.VerifiedChains) == 0 || len(cs.PeerCertificates) == 0 || stockBindingDigest(cs.PeerCertificates[0].Raw) != pin {
			return ErrBoundary
		}
		return nil
	}}
	// Hash each bounded file separately to retain unambiguous file boundaries.
	commitment := ""
	for _, v := range raw {
		commitment += stockBindingDigest(v)
	}
	return stockReadServeMaterial{r, config, stockBindingDigest([]byte(commitment))}, nil
}

func loadStockReadServeMaterial(ctx context.Context, p stockReadProfile, auth stockReadAuthBinding) (stockReadServeMaterial, error) {
	fail := func() (stockReadServeMaterial, error) { return stockReadServeMaterial{}, ErrBoundary }
	if ctx == nil || ctx.Err() != nil || os.Geteuid() != 0 || !validStockReadProfile(p) {
		return fail()
	}
	path := filepath.Join(p.Directory, "supervisor-admin")
	if rootControlledAncestors(path) != nil {
		return fail()
	}
	before, e := os.Lstat(path)
	if e != nil || !before.IsDir() || before.Mode().Perm() != 0700 {
		return fail()
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return fail()
	}
	root, e := os.OpenRoot(path)
	if e != nil {
		return fail()
	}
	defer root.Close()
	opened, e := root.Stat(".")
	if e != nil || !os.SameFile(before, opened) {
		return fail()
	}
	names := [5]string{"read-serve.json", "read-serve-ca.der", "read-serve-server.pem", "read-serve-server.key", "read-serve-client.der"}
	var raw [5][]byte
	defer func() {
		for _, v := range raw {
			clear(v)
		}
	}()
	for i, name := range names {
		raw[i], e = stockReadAuthFile(root, name)
		if e != nil || ctx.Err() != nil {
			return fail()
		}
	}
	stock, e := stockReadAuthFile(root, "stock-api.der")
	if e != nil {
		return fail()
	}
	defer clear(stock)
	result, e := validateStockReadServeMaterial(raw, auth, stock)
	if e != nil {
		return fail()
	}
	for i, name := range names {
		again, err := stockReadAuthFile(root, name)
		same := err == nil && bytes.Equal(again, raw[i])
		clear(again)
		if !same || ctx.Err() != nil {
			return fail()
		}
	}
	final, e := os.Lstat(path)
	if e != nil || !os.SameFile(before, final) || !final.IsDir() || final.Mode().Perm() != 0700 || rootControlledAncestors(path) != nil {
		return fail()
	}
	last, ok := final.Sys().(*syscall.Stat_t)
	if !ok || last.Uid != 0 || last.Ctim != stat.Ctim || ctx.Err() != nil {
		return fail()
	}
	return result, nil
}

// Controller identity comes only from the authenticated committed session record.
func (s *stockReadSession) ControllerID() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.ready || s.life == nil {
		return ""
	}
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if s.life.closed {
		return ""
	}
	return s.life.auth.record.ControllerID
}

// Private to the explicit qualification caller. No arbitrary source, listener, TLS callback,
// handler or scope can be supplied. Opening is exclusively the existing owned
// fixed-9445 lifecycle; any failure terminates it, with no restart or repair.
func serveStockReadOnly(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrBoundary
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	session, e := newStockReadSession(ctx)
	if e != nil {
		return ErrBoundary
	}
	defer session.Close()
	life := session.life
	life.mu.Lock()
	if life.closed || life.binding == nil || life.held == nil {
		life.mu.Unlock()
		return ErrBoundary
	}
	profile, auth, listener := life.binding.observed.profile, life.auth, life.held.listener
	life.mu.Unlock()
	material, e := loadStockReadServeMaterial(ctx, profile, auth)
	if e != nil {
		return ErrBoundary
	}
	handler, e := managedread.New(session, material.review.ClientSHA256, []string{"capacity"}, nil, nil)
	if e != nil {
		return ErrBoundary
	}
	if life.recheck(ctx) != nil {
		return ErrBoundary
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 16384, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan error, 1)
	go func() { done <- server.Serve(tls.NewListener(listener, material.tls)) }()
	defer func() { cancel(); server.Close(); <-done }()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ErrBoundary
		case <-life.done:
			return ErrBoundary
		case err := <-done:
			// Preserve a single joined result for the deferred cleanup.
			done <- err
			return ErrBoundary
		case <-tick.C:
			check, stop := context.WithTimeout(ctx, 5*time.Second)
			e := life.recheck(check)
			latest, loadErr := loadStockReadServeMaterial(check, profile, auth)
			stop()
			if e != nil || loadErr != nil || latest.commitment != material.commitment {
				return ErrBoundary
			}
		}
	}
}
