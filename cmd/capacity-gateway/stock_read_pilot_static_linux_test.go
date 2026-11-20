//go:build linux && stock_read_pilot_harness

package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

// Static fixture bytes only, not a full gateway config or installation recipe.
// Expected commitments are supplied by the separate reviewer. Never emit new
// expected hashes from this renderer or read environment, files or credentials.
type stockPilotStaticInputs struct {
	FacadeSPKI, DormantProviderFingerprint      string
	ReviewedBase, ReviewedOverlay, ReviewedSeed string
}
type stockPilotStaticBytes struct{ Base, Overlay, Seed []byte }

func renderStockPilotStatic(in stockPilotStaticInputs) (stockPilotStaticBytes, error) {
	refuse := func() (stockPilotStaticBytes, error) { return stockPilotStaticBytes{}, errStaticConfig }
	for _, v := range []string{in.FacadeSPKI, in.DormantProviderFingerprint, in.ReviewedBase, in.ReviewedOverlay, in.ReviewedSeed} {
		b, err := hex.DecodeString(v)
		if err != nil || len(b) != 32 || hex.EncodeToString(b) != v {
			return refuse()
		}
	}
	if in.FacadeSPKI == in.DormantProviderFingerprint {
		return refuse()
	}
	const mount = "/run/capacity-private/"
	roleTLS := func(prefix string) securefiles.TLSFiles {
		return securefiles.TLSFiles{CA: mount + prefix + "-ca.pem", Certificate: mount + prefix + ".pem", Key: mount + prefix + ".key"}
	}
	base := stockPilotSyntheticGatewayBase{
		Listen: "127.0.0.1:9443", Controller: "stock-read-pilot-only",
		Database: "/var/lib/capacity-gateway/ledger.db", Identity: mount + "ledger.identity",
		Hosts:     map[string]hostConfiguration{"testhost": {Endpoint: "https://127.0.0.1:9553", Project: "runners", TLS: roleTLS("dormant-host")}},
		Pools:     map[string]gateway.PoolPolicy{"testpool": {Host: "testhost", CPU: 1000, RAM: 1024, Flavor: "small", Image: "linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"base"}, Type: "container", Arch: "amd64"}},
		ServerTLS: roleTLS("gateway-server"), ProviderPins: []string{in.DormantProviderFingerprint},
		Admin: stockPilotSyntheticAdminBase{Listen: "127.0.0.1:9444", TLS: roleTLS("gateway-server"), TokenFile: mount + "admin.token"},
	}
	var overlay stockPilotSyntheticGatewayOverlay
	overlay.Observation, overlay.Garm.Managed = true, true
	overlay.Garm.Endpoint, overlay.Garm.Pin = "https://127.0.0.1:9445", in.FacadeSPKI
	overlay.Garm.Providers = []string{"capacity"}
	overlay.Garm.TLS = securefiles.TLSFiles{CA: mount + "gateway-read-ca.pem", Certificate: mount + "gateway-read-client.pem", Key: mount + "gateway-read-client.key"}
	seed := admission.Config{
		Hosts: map[string]admission.Host{"testhost": {Mode: "disabled", ObservedAt: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)}},
		Pools: map[string]admission.Pool{"testpool": {Hosts: []string{"testhost"}}}, MaxObservationAge: 3 * time.Second,
	}
	baseRaw, err1 := json.Marshal(base)
	overlayRaw, err2 := json.Marshal(overlay)
	seedRaw, err3 := json.Marshal(seed)
	var decodedSeed admission.Config
	if err1 != nil || err2 != nil || err3 != nil || stockPilotCompositionDigest(seedRaw) != in.ReviewedSeed || gatewayrpc.Decode(bytes.NewReader(seedRaw), &decodedSeed) != nil ||
		checkStockPilotSyntheticComposition(baseRaw, overlayRaw, in.ReviewedBase, in.ReviewedOverlay, in.FacadeSPKI, in.DormantProviderFingerprint, decodedSeed) != nil {
		return refuse()
	}
	return stockPilotStaticBytes{Base: baseRaw, Overlay: overlayRaw, Seed: seedRaw}, nil
}

func TestStockPilotStaticRendererUsesIndependentCommitments(t *testing.T) {
	in := stockPilotStaticInputs{
		FacadeSPKI: strings.Repeat("a", 64), DormantProviderFingerprint: strings.Repeat("b", 64),
		ReviewedBase: "7aa289b4ad00fd79899b917b01fbbfa4a7d55048a36665cde68406ac231b06e2", ReviewedOverlay: "05e03cb49b78f3eb6bbfdfcb3a934ffbebdf745938ed567bd20314c734ee2454", ReviewedSeed: "3e4c18da6e2df236a2b67dd65e85838c9d483d517483b34c9d8e32c7d0393b48",
	}
	one, err := renderStockPilotStatic(in)
	if err != nil {
		t.Fatal("reviewed static fixture refused")
	}
	two, err := renderStockPilotStatic(in)
	if err != nil || !reflect.DeepEqual(one, two) {
		t.Fatal("static fixture was not deterministic")
	}
	one.Base[0] = '!'
	if one.Base[0] == two.Base[0] {
		t.Fatal("two rendered fixtures alias")
	}
	for _, change := range []func(*stockPilotStaticInputs){
		func(x *stockPilotStaticInputs) { x.ReviewedBase = x.ReviewedOverlay },
		func(x *stockPilotStaticInputs) { x.ReviewedOverlay = x.ReviewedBase },
		func(x *stockPilotStaticInputs) { x.ReviewedSeed = x.ReviewedBase },
		func(x *stockPilotStaticInputs) { x.FacadeSPKI = strings.Repeat("c", 64) },
		func(x *stockPilotStaticInputs) { x.DormantProviderFingerprint = x.FacadeSPKI },
		func(x *stockPilotStaticInputs) { x.DormantProviderFingerprint = strings.Repeat("c", 64) },
	} {
		wrong := in
		change(&wrong)
		out, err := renderStockPilotStatic(wrong)
		if err == nil || !reflect.DeepEqual(out, stockPilotStaticBytes{}) {
			t.Fatal("changed input produced fixture bytes")
		}
	}
}
