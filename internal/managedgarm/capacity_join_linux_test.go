//go:build linux

package managedgarm

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeenforcement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/profilesnapshot"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCapacityJoinCannotComeFromConfigOrZeroProof(t *testing.T) {
	var b *LoadedStockBinding
	if _, err := b.ObservePoolCapacity(context.Background(), "pool", nil); err == nil {
		t.Fatal("missing loaded binding accepted")
	}
	if (PoolCapacityObservation{}).Valid() || (PoolCapacityObservation{}).Check(context.Background()) == nil {
		t.Fatal("zero observation accepted")
	}
	if validatePoolCapacityJoin(profilesnapshot.Snapshot{}, "pool", []nativeenforcement.Observation{{}}) == nil {
		t.Fatal("unminted proof accepted")
	}
}
func TestCapacityBudgetFitsActualFiniteLimits(t *testing.T) {
	b := admission.Resources{CPU: 1000, RAM: 1024, Disk: 2048, Runners: 2}
	l := nativeenforcement.Limits{CPUMilli: 1000, MemoryBytes: 1024 << 20, DiskBytes: 2048 << 20, Tasks: 128}
	if !budgetFitsEnforcement(b, l) {
		t.Fatal("exact finite budget refused")
	}
	for _, bad := range []admission.Resources{{CPU: 1001, RAM: 1024, Disk: 2048, Runners: 2}, {CPU: 1000, RAM: 1025, Disk: 2048, Runners: 2}, {CPU: 1000, RAM: 1024, Disk: 2049, Runners: 2}, {CPU: 1000, RAM: math.MaxInt64, Disk: 2048, Runners: 2}, {CPU: 1000, RAM: 1024, Disk: 0, Runners: 2}} {
		if budgetFitsEnforcement(bad, l) {
			t.Fatal("unbounded or excessive budget accepted")
		}
	}
	l.MemoryBytes--
	if budgetFitsEnforcement(b, l) {
		t.Fatal("fractional MiB rounded up")
	}
}

func TestCapacityJoinUsesActualMTLSObservationAndExactEnrollment(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, IsCA: true, BasicConstraintsValid: true}
	raw, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	owner := nativeidentity.Descriptor{DomainID: strings.Repeat("a", 64), PolicyDigest: strings.Repeat("b", 64), OwnerClientLeaf: strings.Repeat("c", 64), NativeServerKey: strings.Repeat("d", 64), NativeOrigin: "https://native.example", NativeScope: "scope", NativeServerName: "native", NativeServerVersion: "6"}
	limits := nativeenforcement.Limits{CPUMilli: 2000, MemoryBytes: 2048 << 20, DiskBytes: 4096 << 20, Tasks: 256}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		digest, _ := nativeenforcement.DigestLimits(limits)
		json.NewEncoder(w).Encode(nativeenforcement.Descriptor{Version: nativeenforcement.Version, Nonce: r.URL.Query().Get("nonce"), IssuedAt: time.Now().UTC(), Owner: owner, CapsuleBirthID: owner.DomainID, RootImageSHA256: strings.Repeat("e", 64), DeviceProgramSHA256: strings.Repeat("f", 64), EgressPolicySHA256: strings.Repeat("1", 64), Limits: limits, LimitsDigest: digest})
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, MinVersion: tls.VersionTLS13}
	server.StartTLS()
	defer server.Close()
	pin := sha256.Sum256(server.Certificate().Raw)
	owner.OwnerServerLeaf = hex.EncodeToString(pin[:])
	trust := x509.NewCertPool()
	trust.AddCert(server.Certificate())
	observed, err := nativeenforcement.Observe(context.Background(), nativeenforcement.Configuration{Endpoint: server.URL, Expected: owner, TLS: &tls.Config{RootCAs: trust, Certificates: []tls.Certificate{{Certificate: [][]byte{raw}, PrivateKey: key}}}})
	if err != nil {
		t.Fatal(err)
	}
	budget := admission.Resources{CPU: 2000, RAM: 2048, Disk: 4096, Runners: 2}
	ref := admission.BrokerProfileDomain{Sequence: 2, Host: "host", HostBindingSequence: 1, DomainID: owner.DomainID, PolicyDigest: owner.PolicyDigest}
	domain := admission.NativeDomainBinding{Sequence: 2, Spec: admission.NativeDomainSpec{Controller: "controller", Host: "host", HostBindingSequence: 1, DomainID: owner.DomainID, PolicyDigest: owner.PolicyDigest, OwnerClientLeaf: owner.OwnerClientLeaf, OwnerServerLeaf: owner.OwnerServerLeaf}}
	makeState := func() profilesnapshot.Snapshot {
		return profilesnapshot.Snapshot{Sequence: 3, Controller: "controller", Payload: profilesnapshot.Payload{Profiles: map[string]admission.BrokerPoolProfile{"pool": {Domains: []admission.BrokerProfileDomain{ref}}}, NativeDomains: map[int64]admission.NativeDomainBinding{2: domain}, HostBindings: map[string]int64{"host": 1}, PoolHosts: map[string][]string{"pool": {"host"}}}, Accounting: profilesnapshot.Accounting{DomainOverheads: []admission.NativeDomainOverhead{{Sequence: 3, DomainBindingSequence: 2, Host: "host", DomainID: owner.DomainID, CapsuleBirthID: owner.DomainID, Resources: admission.Resources{CPU: 2000, RAM: 2048, Disk: 4096}, Evidence: observed.Record(), RecordedAt: time.Now()}}, LedgerReady: true, Budgets: admission.Allocations{Global: budget, Hosts: map[string]admission.Resources{"host": budget}, Pools: map[string]admission.Resources{"pool": budget}}, Allocated: admission.Allocations{Global: admission.Resources{CPU: 2000, RAM: 2048, Disk: 4096}, DomainOverhead: admission.Resources{CPU: 2000, RAM: 2048, Disk: 4096}, Hosts: map[string]admission.Resources{"host": {CPU: 2000, RAM: 2048, Disk: 4096}}, Pools: map[string]admission.Resources{}}}}
	}
	for _, mode := range []string{"valid", "missing-overhead", "overhead-owner", "overhead-image", "overhead-raised-limit", "role", "binding", "physical-budget", "unknown-overbudget", "artifact-overbudget", "not-ready", "duplicate", "missing"} {
		t.Run(mode, func(t *testing.T) {
			s := makeState()
			proofs := []nativeenforcement.Observation{observed}
			switch mode {
			case "missing-overhead":
				s.Accounting.DomainOverheads = nil
			case "overhead-owner":
				s.Accounting.DomainOverheads[0].Evidence.Owner.NativeScope = "foreign"
			case "overhead-image":
				s.Accounting.DomainOverheads[0].Evidence.RootImageSHA256 = strings.Repeat("2", 64)
			case "overhead-raised-limit":
				s.Accounting.DomainOverheads[0].Resources.RAM--
			case "role":
				d := domain
				d.Spec.OwnerServerLeaf = strings.Repeat("2", 64)
				s.Payload.NativeDomains[2] = d
			case "binding":
				s.Payload.HostBindings["host"] = 3
			case "physical-budget":
				b := budget
				b.RAM++
				s.Accounting.Budgets.Hosts["host"] = b
			case "unknown-overbudget":
				b := budget
				b.Runners++
				s.Accounting.Allocated.Pools["pool"] = b
			case "not-ready":
				s.Accounting.LedgerReady = false
			case "artifact-overbudget":
				// Current snapshots charge retained shared artifacts to host and
				// global totals, never discount them from physical consumption.
				s.Accounting.Allocated.ArtifactOverhead = admission.Resources{Disk: 1}
				s.Accounting.Allocated.Global.Disk++
				host := s.Accounting.Allocated.Hosts["host"]
				host.Disk++
				s.Accounting.Allocated.Hosts["host"] = host
			case "duplicate":
				proofs = append(proofs, observed)
			case "missing":
				proofs = nil
			}
			err := validatePoolCapacityJoin(s, "pool", proofs)
			if mode == "valid" && err != nil {
				t.Fatal(err)
			}
			if mode != "valid" && err == nil {
				t.Fatal("mismatched authority accepted")
			}
		})
	}
}
