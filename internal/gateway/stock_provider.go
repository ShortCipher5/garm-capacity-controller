package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/cloudbase/garm-provider-common/cloudconfig"
	common "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm-provider-common/util"
)

// StockCommandDigest binds stable nonsecret command identity, never tokens,
// rendered userdata or opaque bootstrap material. Secret-bearing URLs are refused.
// Renewable instance tokens are deliberately not part of replay identity.
func StockCommandDigest(controller, provider string, b common.BootstrapInstance) (string, error) {
	for _, s := range []string{controller, provider, b.Name, b.PoolID, b.Flavor, b.Image} {
		if s == "" || len(s) > 512 || strings.ContainsAny(s, "\x00\r\n") {
			return "", ErrPolicy
		}
	}
	for _, s := range []string{b.RepoURL, b.CallbackURL, b.MetadataURL} {
		u, err := url.Parse(s)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", ErrPolicy
		}
	}
	_, bootstrapMode, err := normalizeStockBootstrap(b)
	if err != nil {
		return "", err
	}
	labels := append([]string{}, b.Labels...)
	sort.Strings(labels)
	selected, err := util.GetTools(b.OSType, b.OSArch, b.Tools)
	if err != nil {
		return "", ErrPolicy
	}
	toolURL, err := url.Parse(selected.GetDownloadURL())
	if err != nil || toolURL.Scheme != "https" || toolURL.Hostname() == "" || toolURL.User != nil || toolURL.RawQuery != "" || toolURL.Fragment != "" {
		return "", ErrPolicy
	}
	// Explicitly remove only the renewable tool credential. The artifact's
	// URL, filename, architecture and checksum still participate in the digest.
	selected.TempDownloadToken = nil
	keys := append([]string{}, b.SSHKeys...)
	v := struct {
		Version, Controller, Provider, Name, Pool, Flavor, Image, Arch, OS, Repo, Callback, Metadata, Group, BootstrapMode string
		JIT                                                                                                                bool
		Labels                                                                                                             []string
		SSHKeys                                                                                                            []string
		CABundle                                                                                                           []byte
		DisableUpdates                                                                                                     bool
		Tool                                                                                                               common.RunnerApplicationDownload
	}{"stock-provider-v1", controller, provider, b.Name, b.PoolID, b.Flavor, b.Image, string(b.OSArch), string(b.OSType), b.RepoURL, b.CallbackURL, b.MetadataURL, b.GitHubRunnerGroup, bootstrapMode, b.JitConfigEnabled, labels, keys, b.CACertBundle, b.UserDataOptions.DisableUpdatesOnBoot, selected}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", ErrPolicy
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// AuthorizesStockScope is startup policy validation, not runtime authority.
func (f *Facade) AuthorizesStockScope(controller, pool string) bool {
	_, ok := f.pools[pool]
	return controller == f.controller && ok
}

// CreateStockProvider is a distinct post-JIT stock-provider origin contract.
// It does not manufacture original-preparation evidence or relax Create.
// Authority MUST originate from the verified leaf's server-side exact scope.
func (f *Facade) CreateStockProvider(ctx context.Context, b common.BootstrapInstance, authority admission.StockProviderAuthority) (common.ProviderInstance, error) {
	empty := common.ProviderInstance{}
	// Runtime lineage is not sole-writer native authority. No qualified capability
	// exists yet, so the new stock path is currently disposable/offline only.
	if f.RuntimeOriginalPreparationRequired() || f.store.CheckOfflineBroker(ctx) != nil {
		return empty, ErrUnavailable
	}
	if authority.Controller != f.controller || authority.PoolID != b.PoolID {
		return empty, ErrPolicy
	}
	req, policy, err := f.request(ctx, b.Name, b.PoolID)
	if err != nil || b.Flavor != policy.Flavor || b.Image != policy.Image || b.OSType != common.Linux || b.OSArch != policy.Arch || b.UserDataOptions.EnableBootDebug || len(b.UserDataOptions.ExtraPackages) != 0 {
		return empty, ErrPolicy
	}
	digest, err := StockCommandDigest(authority.Controller, authority.ProviderName, b)
	if err != nil {
		return empty, err
	}
	var bootstrapMode string
	b, bootstrapMode, err = normalizeStockBootstrap(b)
	if err != nil {
		return empty, ErrPolicy
	}
	tools, err := util.GetTools(b.OSType, b.OSArch, b.Tools)
	if err != nil {
		return empty, ErrPolicy
	}
	var userdata string
	if bootstrapMode == "stock-154638445-linux-wrapper/safe-v1" {
		var normalized struct {
			Wrapper []byte `json:"runner_install_template"`
		}
		if json.Unmarshal(b.ExtraSpecs, &normalized) != nil {
			return empty, ErrPolicy
		}
		b.ExtraSpecs = nil // Already verified; never parse rendered credentials as a Go template.
		userdata, err = cloudconfig.GetCloudInitConfig(b, normalized.Wrapper)
	} else {
		userdata, err = cloudconfig.GetCloudConfig(b, tools, b.Name)
	}
	if err != nil {
		return empty, ErrPolicy
	}
	plan, err := f.store.AdmitStockProviderPlacement(ctx, admission.PlacementRequest{Key: req.Key, Pool: req.Pool, Resources: req.Resources, Execution: executionImage(f.controller, b.Name, policy), Hosts: append([]string(nil), policy.Hosts...), Strategy: policy.Strategy}, authority, digest)
	if err != nil || !f.planAllowed(ctx, plan) {
		return empty, ErrUnavailable
	}
	req = plan.Request
	binding, err := f.boundBackend(ctx, req.Host)
	if err != nil {
		return empty, ErrUnavailable
	}
	spec := incusapi.InstanceSpec{Name: b.Name, Type: plan.Execution.Type, Profiles: append([]string(nil), plan.Execution.Profiles...), ImageFingerprint: plan.Execution.Fingerprint, CPUMillis: req.Resources.CPU, RAMMiB: req.Resources.RAM, DiskMiB: req.Resources.Disk, UserData: userdata, ControllerID: f.controller, PoolID: req.Pool, ReservationKey: req.Key}
	if incusapi.ValidateSpec(spec) != nil {
		return empty, ErrPolicy
	}
	_, err = f.store.ExecuteStockProviderDispatch(ctx, plan, binding, authority, digest, func(ctx context.Context, permit admission.DispatchPermit) (admission.DispatchReceipt, error) {
		created, e := f.hosts[req.Host].Create(ctx, spec)
		receipt := admission.DispatchReceipt{State: "unknown", OperationID: created.OperationID, ResourceID: created.ResourceID}
		if e != nil || created.ResourceID != b.Name {
			return receipt, ErrUnavailable
		}
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		e = f.store.RecordDispatchOutcome(persist, permit, admission.DispatchReceipt{State: "submitted", OperationID: created.OperationID, ResourceID: created.ResourceID})
		cancel()
		if e != nil {
			return receipt, ErrUnavailable
		}
		if f.hosts[req.Host].Wait(ctx, created) != nil {
			return receipt, ErrUnavailable
		}
		if _, e = f.verified(ctx, req, b.Name); e != nil {
			return receipt, ErrUnavailable
		}
		if f.collectCreateEvidence(ctx, req, created) == createEvidenceWriterBlocked {
			return receipt, ErrUnavailable
		}
		receipt.State = "confirmed"
		return receipt, nil
	})
	if err != nil {
		if !errors.Is(err, admission.ErrDispatchClaimed) {
			return empty, ErrUnavailable
		}
		// The ledger checks origin and digest before permitting a replay disposition.
		record, e := f.store.LookupDispatch(ctx, req.Key)
		if e != nil || record.State != "confirmed" || record.ResourceID != b.Name || record.HostBindingSequence != binding.Sequence || record.StockProviderSequence <= 0 {
			return empty, ErrUnavailable
		}
	}
	if _, err = f.verified(ctx, req, b.Name); err != nil {
		return empty, ErrUnavailable
	}
	return common.ProviderInstance{ProviderID: b.Name, Name: b.Name, OSType: common.Linux, OSArch: policy.Arch, Status: common.InstanceRunning}, nil
}
