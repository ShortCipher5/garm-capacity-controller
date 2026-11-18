package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
	common "github.com/cloudbase/garm-provider-common/params"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stockAuthority() admission.StockProviderAuthority {
	return admission.StockProviderAuthority{Controller: "controller-a", PoolID: "pool-a", ProviderName: "stock-incus", ClientFingerprint: strings.Repeat("b", 64)}
}
func TestStockDigestStableAndSecretIndependent(t *testing.T) {
	b := bootstrap("runner-a", "pool-a")
	b.Labels = []string{"z", "a"}
	a, e := StockCommandDigest("controller-a", "stock", b)
	if e != nil {
		t.Fatal(e)
	}
	b.InstanceToken = "renewed-secret"
	b.Labels = []string{"a", "z"}
	c, e := StockCommandDigest("controller-a", "stock", b)
	if e != nil || a != c {
		t.Fatal("renewal changed stable identity")
	}
	b.Flavor = "different"
	c, _ = StockCommandDigest("controller-a", "stock", b)
	if a == c {
		t.Fatal("identity mutation not bound")
	}
	b.MetadataURL += "?token=private"
	if _, e = StockCommandDigest("controller-a", "stock", b); e == nil {
		t.Fatal("secret URL allowed")
	}
}
func TestStockCreateOneShotReplayAndIdentityConflict(t *testing.T) {
	f, s, backend, _ := facadeFixture(t)
	issueFacadeStockGrant(t, f, stockAuthority())
	ctx := context.Background()
	b := bootstrap("runner-a", "pool-a")
	auth := stockAuthority()
	for i := 0; i < 2; i++ {
		b.InstanceToken += "renewed"
		if _, e := f.CreateStockProvider(ctx, b, auth); e != nil {
			t.Fatal(e)
		}
	}
	if backend.calls.Load() != 1 {
		t.Fatal("replay redispatched")
	}
	record, e := s.LookupDispatch(ctx, key("controller-a", "pool-a", "runner-a"))
	if e != nil || record.StockProviderSequence <= 0 || record.OriginalPreparationSequence != 0 {
		t.Fatalf("incorrect origin: %+v %v", record, e)
	}
	b.Labels = []string{"changed"}
	if _, e = f.CreateStockProvider(ctx, b, auth); e == nil {
		t.Fatal("changed command adopted")
	}
	if _, e = f.Create(ctx, b); e == nil {
		t.Fatal("prepared path adopted stock origin")
	}
	if backend.calls.Load() != 1 {
		t.Fatal("conflict redispatched")
	}
}
func TestStockLostPOSTStaysChargedAndNeverRetries(t *testing.T) {
	f, s, backend, _ := facadeFixture(t)
	issueFacadeStockGrant(t, f, stockAuthority())
	backend.createFail = true
	ctx := context.Background()
	b := bootstrap("runner-a", "pool-a")
	for i := 0; i < 2; i++ {
		if _, e := f.CreateStockProvider(ctx, b, stockAuthority()); e == nil {
			t.Fatal("unknown succeeded")
		}
	}
	if backend.calls.Load() != 1 {
		t.Fatal("unknown redispatched")
	}
	snap, e := s.Snapshot(ctx)
	if e != nil || len(snap.Reservations) != 1 {
		t.Fatal("unknown charge lost")
	}
}

func TestStockRuntimeRefusesUnprovenNativeAuthority(t *testing.T) {
	f, s, backend, _ := facadeFixture(t)
	issueFacadeStockGrant(t, f, stockAuthority())
	ctx := context.Background()
	snap, e := s.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	state, ident := t.TempDir(), t.TempDir()
	if e = os.Chmod(state, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(ident, 0700); e != nil {
		t.Fatal(e)
	}
	db, id := filepath.Join(state, "runtime.db"), filepath.Join(ident, "identity")
	if e = admission.InitializeSQLite(db, id, snap.Config); e != nil {
		t.Fatal(e)
	}
	runtime, e := admission.OpenRuntimeSQLite(db, id)
	if e != nil {
		t.Fatal(e)
	}
	defer runtime.Close()
	f.store = runtime
	if _, e = f.CreateStockProvider(ctx, bootstrap("runner-a", "pool-a"), stockAuthority()); e == nil {
		t.Fatal("runtime bypassed missing native authority")
	}
	if backend.calls.Load() != 0 {
		t.Fatal("runtime dispatched")
	}
	after, e := runtime.Snapshot(ctx)
	if e != nil || len(after.Reservations) != 0 {
		t.Fatal("runtime refusal left reservation")
	}
}

func TestStockDigestBindsNonsecretBootstrapSettings(t *testing.T) {
	base := bootstrap("runner-a", "pool-a")
	original, e := StockCommandDigest("controller-a", "stock", base)
	if e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*common.BootstrapInstance){
		func(b *common.BootstrapInstance) { b.SSHKeys = []string{"ssh-ed25519 public-key"} },
		func(b *common.BootstrapInstance) { b.CACertBundle = []byte("public-ca") },
		func(b *common.BootstrapInstance) { b.UserDataOptions.DisableUpdatesOnBoot = true },
		func(b *common.BootstrapInstance) {
			b.Tools = append([]common.RunnerApplicationDownload{}, b.Tools...)
			v := "different.tar.gz"
			b.Tools[0].Filename = &v
		},
	} {
		b := base
		change(&b)
		digest, e := StockCommandDigest("controller-a", "stock", b)
		if e != nil || digest == original {
			t.Fatal("changed nonsecret bootstrap not bound", e)
		}
	}
	token := "renewable-tool-token"
	base.Tools[0].TempDownloadToken = &token
	digest, e := StockCommandDigest("controller-a", "stock", base)
	if e != nil || digest != original {
		t.Fatal("tool credential persisted in digest")
	}
	signed := "https://example.invalid/runner?signature=secret"
	base.Tools[0].DownloadURL = &signed
	if _, e = StockCommandDigest("controller-a", "stock", base); e == nil {
		t.Fatal("signed-query executable identity accepted")
	}
}

func TestStockReservedIntentRejectsChangedBootstrapBeforeFirstDispatch(t *testing.T) {
	f, s, backend, _ := facadeFixture(t)
	issueFacadeStockGrant(t, f, stockAuthority())
	ctx := context.Background()
	b := bootstrap("runner-a", "pool-a")
	identity := backend.identity
	backend.identity = nil
	if _, e := f.CreateStockProvider(ctx, b, stockAuthority()); e == nil {
		t.Fatal("missing backend authority accepted")
	}
	state, e := s.Snapshot(ctx)
	if e != nil || len(state.Reservations) != 1 {
		t.Fatal("fixture did not leave reserved intent")
	}
	backend.identity = identity
	b.SSHKeys = []string{"ssh-ed25519 changed-key"}
	if _, e = f.CreateStockProvider(ctx, b, stockAuthority()); e == nil {
		t.Fatal("changed bootstrap adopted reserved intent")
	}
	if backend.calls.Load() != 0 {
		t.Fatal("changed bootstrap dispatched")
	}
}

func TestStockArbitraryWrapperRemainsUnsupported(t *testing.T) {
	f, s, backend, _ := facadeFixture(t)
	issueFacadeStockGrant(t, f, stockAuthority())
	b := bootstrap("runner-a", "pool-a")
	// Pinned stock GARM MaybeAddWrapperToExtraSpecs always adds this key;
	// this fixture represents its shape, not permission to execute a caller script.
	b.ExtraSpecs = []byte(`{"runner_install_template":"#!/bin/sh\n# rendered metadata wrapper with renewable-token"}`)
	if _, e := f.CreateStockProvider(context.Background(), b, stockAuthority()); e != ErrPolicy {
		t.Fatalf("wrapper not explicitly rejected: %v", e)
	}
	state, e := s.Snapshot(context.Background())
	if e != nil || len(state.Reservations) != 0 || backend.calls.Load() != 0 {
		t.Fatal("unsupported wrapper produced side effects")
	}
}

func TestStockPinnedWrapperSafeRenderAndImmutableMode(t *testing.T) {
	f, _, backend, _ := facadeFixture(t)
	issueFacadeStockGrant(t, f, stockAuthority())
	b := bootstrap("runner-a", "pool-a")
	raw, e := json.Marshal(map[string][]byte{"runner_install_template": expectedStockWrapper(b)})
	if e != nil {
		t.Fatal(e)
	}
	b.ExtraSpecs = raw
	normalized, mode, e := normalizeStockBootstrap(b)
	if e != nil || mode != "stock-154638445-linux-wrapper/safe-v1" {
		t.Fatal("pinned wrapper rejected", e)
	}
	var fields map[string][]byte
	if e = json.Unmarshal(normalized.ExtraSpecs, &fields); e != nil {
		t.Fatal(e)
	}
	script := string(fields["runner_install_template"])
	if strings.Contains(script, "set -ex") || !strings.Contains(script, "set +x\n") || !strings.Contains(script, `"$METADATA_URL/install-script/"`) {
		t.Fatal("unsafe wrapper rendered")
	}
	original, e := StockCommandDigest("controller-a", "stock", b)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.CreateStockProvider(context.Background(), b, stockAuthority()); e != nil {
		t.Fatal("pinned stock create failed", e)
	}
	b.InstanceToken = "renewed-instance-token"
	b.ExtraSpecs, _ = json.Marshal(map[string][]byte{"runner_install_template": expectedStockWrapper(b)})
	renewed, e := StockCommandDigest("controller-a", "stock", b)
	if e != nil || original != renewed {
		t.Fatal("renewable wrapper token changed digest")
	}
	if _, e = f.CreateStockProvider(context.Background(), b, stockAuthority()); e != nil || backend.calls.Load() != 1 {
		t.Fatal("renewal redispatched", e)
	}
	b.ExtraSpecs = []byte("{}")
	plain, e := StockCommandDigest("controller-a", "stock", b)
	if e != nil || plain == original {
		t.Fatal("bootstrap mode not bound")
	}
	for _, extras := range []map[string][]byte{
		{"runner_install_template": append(expectedStockWrapper(b), []byte("echo changed")...)},
		{"runner_install_template": expectedStockWrapper(b), "extra_context": []byte("x")},
	} {
		b.ExtraSpecs, _ = json.Marshal(extras)
		if _, _, e = normalizeStockBootstrap(b); e == nil {
			t.Fatal("custom wrapper or extra key accepted")
		}
	}
}

func TestStockWrapperRejectsDuplicateKeysAndUnsafeTokens(t *testing.T) {
	b := bootstrap("runner-a", "pool-a")
	value, _ := json.Marshal(expectedStockWrapper(b))
	b.ExtraSpecs = []byte(`{"runner_install_template":` + string(value) + `,"runner_install_template":` + string(value) + `}`)
	if _, _, e := normalizeStockBootstrap(b); e == nil {
		t.Fatal("duplicate wrapper key accepted")
	}
	b.InstanceToken = "secret\ncontrol"
	b.ExtraSpecs, _ = json.Marshal(map[string][]byte{"runner_install_template": expectedStockWrapper(b)})
	if _, _, e := normalizeStockBootstrap(b); e == nil {
		t.Fatal("control token accepted")
	}
}

func TestStockSSHKeyOrderRemainsPartOfCommand(t *testing.T) {
	b := bootstrap("runner-a", "pool-a")
	b.SSHKeys = []string{"first-key", "second-key"}
	one, e := StockCommandDigest("controller-a", "stock", b)
	if e != nil {
		t.Fatal(e)
	}
	b.SSHKeys = []string{"second-key", "first-key"}
	two, e := StockCommandDigest("controller-a", "stock", b)
	if e != nil || one == two {
		t.Fatal("key ordering silently normalized")
	}
}

func issueFacadeStockGrant(t *testing.T, f *Facade, auth admission.StockProviderAuthority) {
	t.Helper()
	ctx := context.Background()
	req, p, e := f.request(ctx, "fixture", auth.PoolID)
	if e != nil {
		t.Fatal(e)
	}
	state, e := f.store.ReadBrokerGrantState(ctx)
	if e != nil {
		t.Fatal(e)
	}
	binding, e := f.store.LookupHostBinding(ctx, req.Host)
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256([]byte(t.Name()))
	_, e = f.store.IssueBrokerGrants(ctx, admission.BrokerGrantBatch{ID: hex.EncodeToString(digest[:]), ExpectedSequence: state.Sequence, PolicyRevision: state.Policy.Revision, Grants: []admission.BrokerGrantSpec{{Authority: auth, Host: req.Host, HostBindingSequence: binding.Sequence, Resources: req.Resources, Execution: executionImage(f.controller, "", p), ExpiresAt: time.Now().Add(time.Hour)}}})
	if e != nil {
		t.Fatal(e)
	}
}

func TestStockRequiresGrantAndUnknownCannotCancelCharge(t *testing.T) {
	f, s, backend, _ := facadeFixture(t)
	ctx := context.Background()
	b := bootstrap("runner-a", "pool-a")
	if _, e := f.CreateStockProvider(ctx, b, stockAuthority()); e == nil {
		t.Fatal("ungranted stock accepted")
	}
	state, e := s.ReadSnapshot(ctx)
	if e != nil || state.Allocated.Global.Runners != 0 || backend.calls.Load() != 0 {
		t.Fatal("ungranted side effect")
	}
	issueFacadeStockGrant(t, f, stockAuthority())
	state, e = s.ReadSnapshot(ctx)
	if e != nil || state.Allocated.Global.Runners != 1 {
		t.Fatal("grant not charged")
	}
	backend.createFail = true
	if _, e = f.CreateStockProvider(ctx, b, stockAuthority()); e == nil {
		t.Fatal("unknown create succeeded")
	}
	if _, e = f.CancelAdmission(ctx, garmprotocol.Request{Protocol: garmprotocol.Version, Name: b.Name, PoolID: b.PoolID}); e == nil {
		t.Fatal("unknown canonical dispatch cancelled")
	}
	state, e = s.ReadSnapshot(ctx)
	if e != nil || state.Allocated.Global.Runners != 1 {
		t.Fatal("grant double charged or unknown freed")
	}
	if _, e = f.CreateStockProvider(ctx, b, stockAuthority()); e == nil || backend.calls.Load() != 1 {
		t.Fatal("unknown retried physical create")
	}
}
