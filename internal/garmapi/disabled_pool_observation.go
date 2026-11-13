package garmapi

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DisabledPoolObservation is a fresh, restricted API observation only. It is
// NOT registration, an enable permit, an external writer fence or physical proof.
type DisabledPoolObservation struct {
	Pool              ManagedPool
	ForgeEndpointName string
	ObservedAt        time.Time
}

// ObserveDisabledRepoPool is a trusted-server transport primitive. Its caller
// MUST derive the exact UUID/spec from a confirmed independently bound journal,
// not caller JSON/list similarity. It adds no write scope, reconstructs no POST
// receipt and does not itself attest journal confirmation. Never expose it as a
// raw browser endpoint. It works with AllowWrites:false; the supplied token
// must independently authorize the GET. No upstream permission is granted.
func (m *ManagementClient) ObserveDisabledRepoPool(ctx context.Context, repositoryID, poolID string, spec DisabledPoolSpec) (DisabledPoolObservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	canonical, err := CanonicalDisabledPoolSpec(spec)
	if err != nil || !m.repositories[repositoryID] || !managementID(poolID) || !m.client.providers[canonical.ProviderName] {
		return DisabledPoolObservation{}, ErrManagementUnsafe
	}
	// Timestamp the read start, not response completion: a delayed source must
	// not turn an old snapshot into apparently fresh registration evidence.
	observedAt := time.Now().UTC()
	fields, err := m.exchange(ctx, http.MethodGet, "/repositories/"+url.PathEscape(repositoryID)+"/pools/"+url.PathEscape(poolID), nil)
	if err != nil {
		return DisabledPoolObservation{}, ErrManagement
	}
	if !preparationAliases(fields, []string{"endpoint", "github-runner-group"}) {
		return DisabledPoolObservation{}, ErrManagement
	}
	endpoint, err := row(fields["endpoint"])
	if err != nil || !preparationAliases(endpoint, []string{"name"}) {
		return DisabledPoolObservation{}, ErrManagement
	}
	name, err := textField(endpoint, "name", true)
	if err != nil || !boundedSettingText(name, 256) || strings.TrimSpace(name) != name {
		return DisabledPoolObservation{}, ErrManagement
	}
	pool, err := m.verifyDisabledPool(fields, poolID, repositoryID, canonical)
	if err != nil {
		return DisabledPoolObservation{}, ErrManagement
	}
	if ctx.Err() != nil {
		return DisabledPoolObservation{}, ErrManagement
	}
	return DisabledPoolObservation{Pool: pool, ForgeEndpointName: name, ObservedAt: observedAt}, nil
}
