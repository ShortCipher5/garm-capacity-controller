package stockjit

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
	common "github.com/cloudbase/garm-provider-common/params"
)

// ProtectedBootstrap contains credentials only in memory. Serialization and
// formatting are intentionally redacted; Payload is an explicit secret access.
// It is source evidence, not permission to create, start or release a runner.
type ProtectedBootstrap struct {
	payload     string
	observation Observation
	instance    stockidentity.InstanceObservation
}

func (b ProtectedBootstrap) Valid() bool {
	return b.payload != "" && b.observation.Valid() && b.instance.Valid()
}
func (b ProtectedBootstrap) Payload() string {
	if !b.Valid() {
		return ""
	}
	return b.payload
}
func (b ProtectedBootstrap) Observation() Observation     { return b.observation }
func (b ProtectedBootstrap) String() string               { return "protected stock bootstrap (redacted)" }
func (b ProtectedBootstrap) GoString() string             { return b.String() }
func (b ProtectedBootstrap) MarshalJSON() ([]byte, error) { return nil, ErrPolicy }

// FetchProtected reads the three existing stock metadata credential files with
// the exact instance token at the configured metadata origin. It never modifies
// their values to make them fit protected-runner policy. The image descriptor
// must come from the server's immutable native policy, never provider extraspecs.
func (c *Client) fetchProtected(ctx context.Context, controller string, b common.BootstrapInstance, image protectedboot.ImageDescriptor) (ProtectedBootstrap, error) {
	if c == nil || !c.accepts(controller, b) || protectedboot.ValidateDescriptor(image) != nil {
		return ProtectedBootstrap{}, ErrPolicy
	}
	runner, e := c.readCredential(ctx, b.Name, b.InstanceToken, "runner")
	if e != nil {
		return ProtectedBootstrap{}, e
	}
	id, name, github, ephemeral, e := parseRunner(runner)
	if e != nil || id <= 0 || name != b.Name || github != c.policy.GitHubURL || !ephemeral {
		return ProtectedBootstrap{}, ErrObservation
	}
	files := map[string]string{".runner": base64.StdEncoding.EncodeToString(runner)}
	for _, name := range []string{"credentials", "credentials_rsaparams"} {
		raw, e := c.readCredential(ctx, b.Name, b.InstanceToken, name)
		if e != nil {
			return ProtectedBootstrap{}, e
		}
		files["."+name] = base64.StdEncoding.EncodeToString(raw)
	}
	// Bracket the multi-file read with the exact runner settings. This detects
	// registration changes; it does not claim an upstream transactional snapshot.
	after, e := c.readCredential(ctx, b.Name, b.InstanceToken, "runner")
	if e != nil || !bytes.Equal(runner, after) {
		return ProtectedBootstrap{}, ErrObservation
	}
	jit, e := json.Marshal(files)
	if e != nil {
		return ProtectedBootstrap{}, ErrObservation
	}
	raw, e := json.Marshal(protectedboot.Payload{Version: protectedboot.Version, RunnerName: b.Name, Image: protectedboot.CloneDescriptor(image), JIT: base64.StdEncoding.EncodeToString(jit)})
	if e != nil {
		return ProtectedBootstrap{}, ErrObservation
	}
	if _, e = protectedboot.Decode(raw); e != nil {
		return ProtectedBootstrap{}, ErrObservation
	}
	record := Record{ControllerID: controller, PoolID: b.PoolID, Name: name, RunnerID: id, GitHubURL: github, MetadataOriginDigest: protectedboot.Hash([]byte(c.policy.MetadataURL)), ObservedAt: time.Now().UTC()}
	return ProtectedBootstrap{payload: string(raw), observation: Observation{record: record, verified: true}}, nil
}
func (c *Client) accepts(controller string, b common.BootstrapInstance) bool {
	return c != nil && controller == c.policy.ControllerID && b.PoolID == c.policy.PoolID && b.JitConfigEnabled && b.MetadataURL == c.policy.MetadataURL && b.RepoURL == c.policy.GitHubURL && label(b.Name) && len(b.Name) <= 128 && len(b.InstanceToken) > 0 && len(b.InstanceToken) <= 16384 && strings.IndexFunc(b.InstanceToken, func(r rune) bool { return r < 33 || r > 126 }) < 0
}
func (c *Client) readCredential(ctx context.Context, runnerName, token, name string) ([]byte, error) {
	if name != "runner" && name != "credentials" && name != "credentials_rsaparams" {
		return nil, ErrPolicy
	}
	if c.managed != nil {
		out, e := c.managed.ReadCredential(ctx, c.policy.PoolID, runnerName, name, token)
		if e != nil || c.managed.Identity() != c.managedIdentity {
			return nil, ErrObservation
		}
		return out, nil
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, c.policy.MetadataURL+"/credentials/"+name, nil)
	if e != nil {
		return nil, ErrObservation
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/octet-stream")
	response, e := c.http.Do(req)
	if e != nil {
		return nil, ErrObservation
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ErrObservation
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, (1<<19)+1))
	if e != nil || len(raw) == 0 || len(raw) > 1<<19 {
		return nil, ErrObservation
	}
	return raw, nil
}

// ConfiguredSourceIdentity is the same endpoint fingerprint used by stock pool
// ownership observations. It is configuration linkage, not a remote identity or
// activation proof. Path-prefixed metadata origins are unsupported by New.
func (c *Client) ConfiguredSourceIdentity() string {
	if c == nil {
		return ""
	}
	if c.managed != nil {
		return c.managedIdentity
	}
	u, e := url.Parse(c.policy.MetadataURL)
	if e != nil {
		return ""
	}
	u.Path = "/api/v1"
	return protectedboot.Hash([]byte("garm-v0.2.1-management\x00" + u.String()))
}
func (c *Client) AuthorizesControllerPool(controller, pool string) bool {
	return c != nil && c.policy.ControllerID == controller && c.policy.PoolID == pool
}

// FetchProtected requires the actual stock instance identity before and after
// credential retrieval. The returned opaque proof cannot be constructed from
// provider-supplied pool IDs or a metadata file with only a matching name.
func (c *Client) FetchProtected(ctx context.Context, controller string, b common.BootstrapInstance, image protectedboot.ImageDescriptor, source stockidentity.Configuration) (ProtectedBootstrap, error) {
	if c == nil || !c.accepts(controller, b) || source.Pool != b.PoolID || protectedboot.ValidateDescriptor(image) != nil {
		return ProtectedBootstrap{}, ErrPolicy
	}
	before, e := stockidentity.ObserveInstance(ctx, source, b.Name)
	if e != nil || before.SourceIdentity() != c.ConfiguredSourceIdentity() {
		return ProtectedBootstrap{}, ErrObservation
	}
	result, e := c.fetchProtected(ctx, controller, b, image)
	if e != nil {
		return ProtectedBootstrap{}, e
	}
	after, e := stockidentity.ObserveInstance(ctx, source, b.Name)
	if e != nil || after.SourceIdentity() != before.SourceIdentity() || after.Facts() != before.Facts() || result.observation.Record().RunnerID != after.Facts().AgentID {
		return ProtectedBootstrap{}, ErrObservation
	}
	result.instance = after
	return result, nil
}
func (b ProtectedBootstrap) Instance() stockidentity.InstanceObservation { return b.instance }

// NewManaged binds metadata delivery to a real scoped supervisor ownership read.
// MetadataURL still binds the original stock bootstrap contract; no request is
// sent there by this client. Credentials travel only to the pinned supervisor.
func NewManaged(ctx context.Context, policy Policy, source stockidentity.Configuration) (*Client, error) {
	if source.ManagedServerKeyPin == "" || source.ManagedControllerID != policy.ControllerID || source.Pool != policy.PoolID || source.Token != "" {
		return nil, ErrPolicy
	}
	observed, e := stockidentity.Observe(ctx, source)
	if e != nil || observed.ControllerID() != policy.ControllerID || observed.SourceIdentity() == "" {
		return nil, ErrObservation
	}
	c, e := New(policy, source.TLS)
	if e != nil {
		return nil, e
	}
	c.http.CloseIdleConnections()
	c.http = nil
	c.managed, e = managedsource.New(source.Endpoint, source.ManagedServerKeyPin, source.ManagedControllerID, source.TLS)
	if e != nil {
		return nil, ErrPolicy
	}
	c.managedIdentity = observed.SourceIdentity()
	return c, nil
}
