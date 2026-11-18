//go:build linux

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecontrol"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativedomain"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type controlConfig struct {
	Listen               string               `json:"listen"`
	Policy               nativeapi.PolicySpec `json:"policy"`
	ControllerClientLeaf string               `json:"controller_client_leaf"`
	ControllerServerLeaf string               `json:"controller_server_leaf"`
	ControllerURL        string               `json:"controller_url"`
	ServerCertificate    string               `json:"server_certificate"`
	ServerKey            string               `json:"server_key"`
	OwnerCertificate     string               `json:"owner_certificate"`
	OwnerKey             string               `json:"owner_key"`
	ControllerCA         string               `json:"controller_ca"`
}

func rootData(path string, secret bool, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errOwner
	}
	for p := path; ; p = filepath.Dir(p) {
		st, e := os.Lstat(p)
		if e != nil || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0022 != 0 {
			return nil, errOwner
		}
		s, ok := st.Sys().(*syscall.Stat_t)
		if !ok || s.Uid != 0 {
			return nil, errOwner
		}
		if p == path {
			if !st.Mode().IsRegular() || secret && st.Mode().Perm()&0077 != 0 {
				return nil, errOwner
			}
		} else if !st.IsDir() {
			return nil, errOwner
		}
		if p == "/" {
			break
		}
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, errOwner
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(b)) > limit {
		return nil, errOwner
	}
	return b, nil
}
func loadControl(path string) (controlConfig, error) {
	var c controlConfig
	raw, e := rootData(path, true, 4<<20)
	if e != nil {
		return c, e
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return c, errOwner
	}
	host, port, e := net.SplitHostPort(c.Listen)
	if e != nil || net.ParseIP(host) == nil {
		return c, errOwner
	}
	n, e := strconv.Atoi(port)
	if e != nil || n < 1 || n > 65535 || c.Policy.DomainID != "" || c.Policy.Version != nativeapi.ProtectedPolicyVersion {
		return c, errOwner
	}
	return c, nil
}
func rootPair(cert, key string) (tls.Certificate, error) {
	c, e := rootData(cert, false, 1<<20)
	if e != nil {
		return tls.Certificate{}, e
	}
	k, e := rootData(key, true, 1<<20)
	if e != nil {
		return tls.Certificate{}, e
	}
	pair, e := tls.X509KeyPair(c, k)
	if e != nil {
		return tls.Certificate{}, errOwner
	}
	return pair, nil
}

// serveConfiguredOwner is experimental opt-in code, never an enable-at-boot path.
// It requires real namespace/storage proofs and actual fresh native birth. A
// setup error quarantines the existing daemon rather than killing or adopting it.
func serveConfiguredOwner(ctx context.Context, path string, domain *nativedomain.Domain, boundary *preparedBoundary) error {
	if domain == nil || boundary == nil {
		return errOwner
	}
	config, e := loadControl(path)
	if e != nil {
		return e
	}
	config.Policy.DomainID = domain.Birth().ID
	policy, e := nativeapi.NewPolicy(config.Policy)
	if e != nil {
		return e
	}
	api, e := nativeapi.NewBounded(ctx, domain, policy, boundary.group, boundary.storage, boundary.reader)
	if e != nil {
		return e
	}
	serverCert, e := rootPair(config.ServerCertificate, config.ServerKey)
	if e != nil {
		return e
	}
	ownerCert, e := rootPair(config.OwnerCertificate, config.OwnerKey)
	if e != nil {
		return e
	}
	ca, e := rootData(config.ControllerCA, false, 1<<20)
	if e != nil {
		return e
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return errOwner
	}
	serverTLS := &tls.Config{Certificates: []tls.Certificate{serverCert}, ClientCAs: roots, MinVersion: tls.VersionTLS13}
	ownerTLS := &tls.Config{Certificates: []tls.Certificate{ownerCert}, RootCAs: roots, MinVersion: tls.VersionTLS13}
	claims, e := nativecontrol.NewClaimClient(nativecontrol.ClaimConfiguration{Endpoint: config.ControllerURL, TLS: ownerTLS, ControllerServerLeaf: config.ControllerServerLeaf, OwnerClientLeaf: nativeidentity.Fingerprint(ownerCert.Certificate[0]), DomainID: policy.DomainID(), PolicyDigest: policy.Digest()})
	if e != nil {
		return e
	}
	defer claims.Close()
	server, e := nativecontrol.NewExecutionServer(api, policy, claims, boundary.reader, serverTLS, ownerTLS, config.ControllerClientLeaf)
	if e != nil {
		return e
	}
	listener, e := net.Listen("tcp", config.Listen)
	if e != nil {
		return errOwner
	}
	defer listener.Close()
	ended := make(chan error, 1)
	go func() { ended <- server.ServeTLS(listener, "", "") }()
	nativeEnded := make(chan error, 1)
	go func() { nativeEnded <- domain.Wait(ctx) }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case e := <-ended:
		if e == nil {
			return errors.New("owner server ended; native domain quarantined")
		}
		return e
	case e := <-nativeEnded:
		return e
	}
}
