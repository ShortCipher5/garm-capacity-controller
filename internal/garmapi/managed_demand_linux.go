package garmapi

import (
	"crypto/tls"

	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
)

// NewManagedDemand reads scoped demand through the private pinned supervisor.
// Identity remains empty until an authenticated response supplies real source
// provenance. This client has neither a stock credential nor a write transport.
func NewManagedDemand(endpoint, serverKeyPin, expectedControllerID string, trust *tls.Config, providers, repositoryIDs []string) (*DemandClient, error) {
	return NewManagedDemandWithServerName(endpoint, serverKeyPin, expectedControllerID, "", trust, providers, repositoryIDs)
}

func NewManagedDemandWithServerName(endpoint, serverKeyPin, expectedControllerID, serverName string, trust *tls.Config, providers, repositoryIDs []string) (*DemandClient, error) {
	source, err := managedsource.NewWithServerName(endpoint, serverKeyPin, expectedControllerID, serverName, trust)
	if err != nil {
		return nil, ErrInventory
	}
	c, err := NewDemand(endpoint, "managed-mtls-not-a-stock-token", trust, providers, repositoryIDs)
	if err != nil {
		return nil, err
	}
	m := c.discovery.management
	m.client.http.Transport = source
	m.managedSource = source
	c.registrationOwner = &runningRegistrationOwner{}
	return c, nil
}
