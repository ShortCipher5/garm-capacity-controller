package reuse

import (
	"strings"
	"time"
)

// ChargeRef is the ledger reservation a charge belongs to. The key is the
// reservation identity; pool and host locate its scope.
type ChargeRef struct {
	Key     string `json:"key"`
	Pool    string `json:"pool"`
	Host    string `json:"host"`
	CPU     int64  `json:"cpu_millis"`
	RAM     int64  `json:"ram_mib"`
	Disk    int64  `json:"disk_mib"`
	Runners int64  `json:"runners"`
}

// StockBinding is the original stock registration that incurred the charge:
// the exact source metadata origin, controller and pool enrollment, and the
// runner registration identity. The display name alone is never identity.
type StockBinding struct {
	// RegistrationDigest binds the exact authenticated registration record
	// (controller, pool, name, runner ID, repository URL, metadata origin).
	RegistrationDigest string `json:"registration_digest"`
	ControllerID       string `json:"controller_id"`
	PoolID             string `json:"pool_id"`
	// GitHubURL is the exact "https://host/owner/name" repository the runner
	// registered for. Two competing repositories never share a charge.
	GitHubURL  string `json:"github_url"`
	RunnerName string `json:"runner_name"`
	RunnerID   int64  `json:"runner_id"`
}

// ProviderBinding is the enrolled daemon, project and member whose
// authenticated transport minted the incarnation observation. A binding
// tied to another reservation, project, daemon or version is a different
// charge and never qualifies this one.
type ProviderBinding struct {
	BackendKeyFingerprint string `json:"backend_key_fingerprint"`
	OriginFingerprint     string `json:"origin_fingerprint"`
	ScopeFingerprint      string `json:"scope_fingerprint"`
	ServerName            string `json:"server_name"`
	ServerVersion         string `json:"server_version"`
	Project               string `json:"project"`
	Member                string `json:"member"`
}

// IncarnationBinding is the immutable instance incarnation: UUID, generation
// and creation time. A replacement that reuses the display name carries a
// different incarnation and is therefore a distinct charge.
type IncarnationBinding struct {
	Name       string    `json:"name"`
	UUID       string    `json:"uuid"`
	Generation string    `json:"generation"`
	CreatedAt  time.Time `json:"created_at"`
}

// VolumeCharge is one applicable exact charged allocation (for the stock
// Incus provider the runner root volume, whose name is the provider ID).
type VolumeCharge struct {
	Name          string `json:"name"`
	BindingDigest string `json:"binding_digest"`
}

// ChargeIdentity pins the ORIGINAL charge: exact stock source, pool and
// runner registration/row, authenticated provider allocation, enrolled
// daemon/project/member, immutable instance incarnation and the applicable
// exact volumes or other charged allocation. Constructing one authorizes
// nothing; validators only decide whether evidence addresses this charge.
type ChargeIdentity struct {
	Ref         ChargeRef          `json:"ref"`
	Stock       StockBinding       `json:"stock"`
	Provider    ProviderBinding    `json:"provider"`
	Incarnation IncarnationBinding `json:"incarnation"`
	Volumes     []VolumeCharge     `json:"volumes"`
}

func (c ChargeIdentity) valid() bool {
	if c.Ref.Key == "" || len(c.Ref.Key) > 256 || !label(c.Ref.Pool) || !label(c.Ref.Host) ||
		c.Ref.CPU <= 0 || c.Ref.RAM <= 0 || c.Ref.Disk < 0 || c.Ref.Runners != 1 {
		return false
	}
	if !digest64(c.Stock.RegistrationDigest) || !label(c.Stock.ControllerID) || !label(c.Stock.PoolID) ||
		!label(c.Stock.RunnerName) || c.Stock.RunnerID <= 0 {
		return false
	}
	if _, _, _, ok := parseGitHubURL(c.Stock.GitHubURL); !ok {
		return false
	}
	if !digest64(c.Provider.BackendKeyFingerprint) || !digest64(c.Provider.OriginFingerprint) ||
		!digest64(c.Provider.ScopeFingerprint) || !label(c.Provider.ServerName) ||
		c.Provider.ServerVersion != "6.0.4" || !label(c.Provider.Project) ||
		(c.Provider.Member != "" && !label(c.Provider.Member)) {
		return false
	}
	if !instanceName(c.Incarnation.Name) || !incarnationID(c.Incarnation.UUID) ||
		!incarnationID(c.Incarnation.Generation) || c.Incarnation.CreatedAt.IsZero() {
		return false
	}
	if len(c.Volumes) > 16 {
		return false
	}
	seen := map[string]bool{}
	for _, v := range c.Volumes {
		if !volumeName(v.Name) || !digest64(v.BindingDigest) || seen[v.Name] {
			return false
		}
		seen[v.Name] = true
	}
	if c.Ref.Disk > 0 && len(c.Volumes) == 0 {
		return false
	}
	return true
}

// Valid reports grammar and cross-field binding only. It is not evidence.
func (c ChargeIdentity) Valid() bool { return c.valid() }

// Digest is the canonical identity of the charge for dedupe and replay.
func (c ChargeIdentity) Digest() string { return canonicalDigest(c) }

// sameIncarnation reports whether two charges bind the same immutable
// instance incarnation on the same enrolled daemon scope.
func sameIncarnation(a, b ChargeIdentity) bool {
	return a.Provider.BackendKeyFingerprint == b.Provider.BackendKeyFingerprint &&
		a.Provider.OriginFingerprint == b.Provider.OriginFingerprint &&
		a.Provider.ScopeFingerprint == b.Provider.ScopeFingerprint &&
		a.Provider.Project == b.Provider.Project &&
		a.Incarnation.UUID == b.Incarnation.UUID &&
		a.Incarnation.Generation == b.Incarnation.Generation &&
		a.Incarnation.CreatedAt.Equal(b.Incarnation.CreatedAt)
}

// SameCharge reports whether two charges are the same original charge. The
// display name is deliberately excluded: a replacement reusing the name is
// a distinct charge. Registration and provider scope must match exactly.
func SameCharge(a, b ChargeIdentity) bool {
	return a.Ref.Key == b.Ref.Key &&
		a.Stock.RegistrationDigest == b.Stock.RegistrationDigest &&
		a.Stock.RunnerID == b.Stock.RunnerID &&
		sameIncarnation(a, b)
}

// DisplayNameReused reports whether b reuses a's display name while binding
// a different original charge (a replacement under the same name).
func DisplayNameReused(a, b ChargeIdentity) bool {
	return a.Incarnation.Name == b.Incarnation.Name && !SameCharge(a, b)
}

func parseGitHubURL(raw string) (host, owner, name string, ok bool) {
	if !strings.HasPrefix(raw, "https://") {
		return "", "", "", false
	}
	rest := strings.TrimPrefix(raw, "https://")
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[0] == "" || !segment(parts[1]) || !segment(parts[2]) {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}
