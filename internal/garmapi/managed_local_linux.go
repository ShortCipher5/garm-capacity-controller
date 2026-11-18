package garmapi

import (
	"crypto/tls"
	"crypto/x509"

	"github.com/ShortCipher5/garm-capacity-controller/internal/managedgarm"
)

// NewManagedLocal reuses the reviewed stock settings parser/guards over the
// concrete root-supervisor session. No arbitrary custom transport or HTTP URL
// can gain this route. Session itself rechecks actual topology each operation
// and refuses anything except reads, pauses and freshly checked strict maximum reductions.
func NewManagedLocal(session *managedgarm.Session, providers, pools, repositories []string) (*ManagementClient, error) {
	if session == nil || session.Identity() == "" {
		return nil, ErrManagement
	}
	m, e := NewManagement(ManagementConfig{Endpoint: "https://managed-garm.invalid", Token: "supervisor-session-owned", Trust: &tls.Config{RootCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS12}, Providers: providers, PoolIDs: pools, RepositoryIDs: repositories, AllowWrites: true})
	if e != nil {
		return nil, e
	}
	m.client.http.Transport = session
	m.managedIdentity = session.Identity()
	return m, nil
}
