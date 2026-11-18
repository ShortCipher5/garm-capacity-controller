package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedgarm"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedwriter"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

type configuration struct {
	SourceInspectorLeaf            string                    `json:"source_inspector_leaf,omitempty"`
	SourceInspectorCertificateFile string                    `json:"source_inspector_certificate_file,omitempty"`
	ClientCertificateFile          string                    `json:"client_certificate_file"`
	Boundary                       managedgarm.Specification `json:"boundary"`
	TLS                            securefiles.TLSFiles      `json:"tls"`
	ClientLeaf                     string                    `json:"client_leaf"`
	Journal                        string                    `json:"journal"`
	JournalLineage                 string                    `json:"journal_lineage"`
	Providers                      []string                  `json:"providers"`
	Pools                          []string                  `json:"pools"`
	Repositories                   []string                  `json:"repositories"`
}

func run(ctx context.Context, args []string) error {
	if len(args) != 1 || os.Geteuid() != 0 {
		return managedgarm.ErrBoundary
	}
	if managedgarm.ValidateSupervisorFilePath(args[0]) != nil {
		return managedgarm.ErrBoundary
	}
	raw, e := securefiles.Read(args[0], 1<<20)
	if e != nil {
		return e
	}
	var c configuration
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return managedgarm.ErrBoundary
	}
	for _, path := range []string{c.TLS.CA, c.TLS.Certificate, c.TLS.Key, c.ClientCertificateFile, c.Journal} {
		if managedgarm.ValidateSupervisorFilePath(path) != nil {
			return managedgarm.ErrBoundary
		}
	}
	trust, e := securefiles.LoadTLS(c.TLS)
	if e != nil {
		return e
	}
	if len(trust.Certificates) != 1 || len(trust.Certificates[0].Certificate) == 0 {
		return managedgarm.ErrBoundary
	}
	serverLeaf := sha256.Sum256(trust.Certificates[0].Certificate[0])
	if c.ClientLeaf == hex.EncodeToString(serverLeaf[:]) || c.ClientLeaf == c.Boundary.GarmServerFingerprint {
		return managedgarm.ErrBoundary
	}
	clientCert, e := readRoleCertificate(c.ClientCertificateFile, c.ClientLeaf)
	if e != nil {
		return e
	}
	stockCert, e := readRoleCertificate(filepath.Join(c.Boundary.AdminSecretDirectory, "api-cert.pem"), c.Boundary.GarmServerFingerprint)
	if e != nil {
		return e
	}
	serverCert, e := x509.ParseCertificate(trust.Certificates[0].Certificate[0])
	if e != nil || !separateRoleKeys(clientCert, serverCert, stockCert) {
		return managedgarm.ErrBoundary
	}
	if (c.SourceInspectorLeaf == "") != (c.SourceInspectorCertificateFile == "") {
		return managedgarm.ErrBoundary
	}
	if c.SourceInspectorLeaf != "" {
		inspector, e := readRoleCertificate(c.SourceInspectorCertificateFile, c.SourceInspectorLeaf)
		if e != nil || !separateRoleKeys(inspector, serverCert, stockCert) || bytes.Equal(inspector.RawSubjectPublicKeyInfo, clientCert.RawSubjectPublicKeyInfo) {
			return managedgarm.ErrBoundary
		}
	}
	trust.ClientCAs = trust.RootCAs
	trust.ClientAuth = tls.RequireAndVerifyClientCert
	session, e := managedgarm.OpenSession(ctx, c.Boundary)
	if e != nil {
		return e
	}
	defer session.Close()
	journal, e := garmmanagement.OpenJournal(c.Journal, c.JournalLineage)
	if e != nil {
		return e
	}
	defer journal.Close()
	handler, e := managedwriter.New(session, journal, c.ClientLeaf, c.Providers, c.Pools, c.Repositories)
	if e != nil {
		return e
	}
	if c.SourceInspectorLeaf != "" {
		if e := handler.SetSourceInspector(c.SourceInspectorLeaf); e != nil {
			return e
		}
	}
	listener, e := session.Listen(ctx)
	if e != nil {
		return e
	}
	defer listener.Close()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 16384}
	go func() { <-ctx.Done(); server.Close() }()
	e = server.Serve(tls.NewListener(listener, trust))
	if e == http.ErrServerClosed {
		return nil
	}
	return e
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if run(ctx, os.Args[1:]) != nil {
		fmt.Fprintln(os.Stderr, "managed stock writer unavailable; no capacity increase authorized")
		os.Exit(1)
	}
}

func readRoleCertificate(path, pin string) (*x509.Certificate, error) {
	if managedgarm.ValidateSupervisorFilePath(path) != nil {
		return nil, managedgarm.ErrBoundary
	}
	raw, e := securefiles.Read(path, 16384)
	if e != nil {
		return nil, managedgarm.ErrBoundary
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" || strings.TrimSpace(string(rest)) != "" {
		return nil, managedgarm.ErrBoundary
	}
	cert, e := x509.ParseCertificate(block.Bytes)
	if e != nil {
		return nil, managedgarm.ErrBoundary
	}
	sum := sha256.Sum256(cert.Raw)
	if hex.EncodeToString(sum[:]) != pin {
		return nil, managedgarm.ErrBoundary
	}
	return cert, nil
}
func separateRoleKeys(client, server, stock *x509.Certificate) bool {
	if client == nil || server == nil || stock == nil || len(client.RawSubjectPublicKeyInfo) == 0 || len(server.RawSubjectPublicKeyInfo) == 0 || len(stock.RawSubjectPublicKeyInfo) == 0 {
		return false
	}
	return !bytes.Equal(client.RawSubjectPublicKeyInfo, server.RawSubjectPublicKeyInfo) && !bytes.Equal(client.RawSubjectPublicKeyInfo, stock.RawSubjectPublicKeyInfo) && !bytes.Equal(server.RawSubjectPublicKeyInfo, stock.RawSubjectPublicKeyInfo)
}
