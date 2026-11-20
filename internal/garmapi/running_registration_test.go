package garmapi

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
)

const registrationRowID = "44444444-4444-4444-8444-444444444444"
const registrationOtherRowID = "55555555-5555-4555-8555-555555555555"
const registrationController = "11111111-1111-4111-8111-111111111111"
const registrationForeignRepo = "66666666-6666-4666-8666-666666666666"
const registrationAgentID int64 = 9007199254740993

func registrationFixturePool(id, repo string) map[string]any {
	p := demandPool(id)
	p["repo_id"], p["image"], p["flavor"], p["os_type"], p["os_arch"] = repo, "fixture-image", "fixture-small", "linux", "amd64"
	return p
}

func registrationFixtureInstance(id, pool, name, providerID string) map[string]any {
	return map[string]any{"id": id, "generation": 0, "name": name, "provider_id": providerID, "provider_name": "capacity",
		"pool_id": pool, "status": "running", "runner_status": "idle", "agent_id": registrationAgentID}
}

type registrationFixture struct {
	c        *DemandClient
	reopen   func() *DemandClient
	enrolled ManagedRunningRegistrationScope
	gets     atomic.Int32
	poolGets atomic.Int32
	flipped  atomic.Bool
}

func newRegistrationFixture(t *testing.T, ordinary bool, modify func(string, int, []map[string]any) []map[string]any) *registrationFixture {
	t.Helper()
	f := &registrationFixture{}
	witness := strings.Repeat("a", 64)
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.gets.Add(1)
		if r.Method != "GET" || r.URL.RawQuery != "" || (!ordinary && r.Header.Get("Authorization") != "") {
			t.Error("unexpected route, query or credential")
			w.WriteHeader(403)
			return
		}
		rawSource := witness
		if f.flipped.Load() {
			rawSource = strings.Repeat("b", 64)
		}
		w.Header().Set(managedsource.SourceHeader, rawSource)
		w.Header().Set(managedsource.ControllerHeader, registrationController)
		rows := []map[string]any{}
		var poolGets int
		switch r.URL.Path {
		case "/api/v1/pools":
			poolGets = int(f.poolGets.Add(1))
			rows = append(rows, registrationFixturePool(managedPoolID, managedRepoID))
		case "/api/v1/jobs":
		case "/api/v1/instances":
			rows = append(rows, registrationFixtureInstance(registrationRowID, managedPoolID, "private-runner-name", "private-provider-object"))
		default:
			t.Error("added an unexpected GET")
			w.WriteHeader(404)
			return
		}
		if modify != nil {
			rows = modify(r.URL.Path, poolGets, rows)
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	trust.Certificates = []tls.Certificate{server.TLS.Certificates[0]}
	sum := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
	pin := hex.EncodeToString(sum[:])
	// Independent fixture enrollment: neither the source pin nor the pool
	// fingerprint is obtained from the candidate demand observation.
	sourceHash := sha256.Sum256([]byte("managed-stock-source/v1\x00" + server.URL + "\x00" + pin + "\x00" + witness + "\x00" + registrationController))
	f.enrolled = ManagedRunningRegistrationScope{SourceIdentity: hex.EncodeToString(sourceHash[:]), ControllerID: registrationController,
		RepositoryID: managedRepoID, PoolID: managedPoolID, ProviderName: "capacity",
		OwnershipFingerprint: (stockidentity.Facts{Pool: managedPoolID, Repository: managedRepoID, Provider: "capacity", Flavor: "fixture-small", Image: "fixture-image", OSType: "linux", OSArch: "amd64"}).Fingerprint()}
	f.reopen = func() *DemandClient {
		var c *DemandClient
		var err error
		if ordinary {
			c, err = NewDemand(server.URL, "fixture", trust, []string{"capacity"}, []string{managedRepoID})
		} else {
			c, err = NewManagedDemand(server.URL, pin, registrationController, trust, []string{"capacity"}, []string{managedRepoID})
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(c.Close)
		return c
	}
	f.c = f.reopen()
	return f
}

func TestManagedRunningRegistrationReportIsExactPrivateAndFresh(t *testing.T) {
	f := newRegistrationFixture(t, false, nil)
	report, err := f.c.ObserveRunningRegistration(t.Context(), f.enrolled, registrationRowID)
	if id, at, ok := report.ReportedAgentID(f.enrolled, registrationRowID); err != nil || !ok || id != registrationAgentID ||
		at.IsZero() || at.After(time.Now()) || f.gets.Load() != 4 {
		t.Fatalf("expected one exact report using four original GETs: %v, %v, %v", ok, err, f.gets.Load())
	}
	// Keep synthetic cutoff inputs separate from the actual TLS capture;
	// nothing about the real report's private times is directly inspectable.
	started := time.Now().Add(-time.Second)
	finished := started.Add(time.Second)
	timed := sealRunningRegistration(f.c.registrationOwner, f.c.discovery.management.managedSource,
		f.enrolled, registrationRowID, registrationAgentID, started, started, finished)
	before := started.Add(runningRegistrationMaxAge - time.Nanosecond)
	if _, _, ok := timed.agentIDAt(f.enrolled, registrationRowID, before); !ok {
		t.Fatal("fresh capture refused")
	}
	for _, at := range []time.Time{finished.Add(-time.Nanosecond), started.Add(runningRegistrationMaxAge)} {
		if id, when, ok := timed.agentIDAt(f.enrolled, registrationRowID, at); ok || id != 0 || !when.IsZero() {
			t.Fatal("stale or backward evidence escaped")
		}
	}
	wrong := f.enrolled
	wrong.OwnershipFingerprint = strings.Repeat("b", 64)
	if _, _, ok := report.ReportedAgentID(wrong, registrationRowID); ok {
		t.Fatal("report accepted a different expected binding")
	}
	if _, _, ok := report.ReportedAgentID(f.enrolled, "private-runner-name"); ok {
		t.Fatal("runner name accepted as lookup key")
	}
	nested := struct {
		Private []ManagedRunningRegistrationReport
	}{[]ManagedRunningRegistrationReport{report}}
	for _, v := range []any{report, &report, nested, &nested, map[string]ManagedRunningRegistrationReport{"private": report}} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			rendered := fmt.Sprintf(format, v)
			for _, hidden := range []string{fmt.Sprint(registrationAgentID), registrationRowID, f.enrolled.SourceIdentity,
				f.enrolled.ControllerID, f.enrolled.RepositoryID, f.enrolled.PoolID, f.enrolled.ProviderName,
				f.enrolled.OwnershipFingerprint, "private-runner-name", "private-provider-object"} {
				if strings.Contains(rendered, hidden) {
					t.Fatalf("private report exposed through format %s", format)
				}
			}
		}
	}
	if data, err := json.Marshal(nested); err != nil || string(data) != `{"Private":[{}]}` {
		t.Fatal("nested report is not wire-opaque")
	}
	payload, err := json.Marshal(report)
	if err != nil || string(payload) != "{}" {
		t.Fatal("report is not wire-opaque")
	}
	var detached ManagedRunningRegistrationReport
	if err := json.Unmarshal(payload, &detached); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := detached.ReportedAgentID(f.enrolled, registrationRowID); ok {
		t.Fatal("JSON manufactured a report")
	}
	f.flipped.Store(true)
	if _, err := f.c.Observe(t.Context()); err != ErrManagement {
		t.Fatal("actual source drift was accepted", err)
	}
	if _, _, ok := report.ReportedAgentID(f.enrolled, registrationRowID); ok {
		t.Fatal("report survived detected source replacement")
	}
}

func TestManagedRunningRegistrationRefusesUnknownAndContradictoryScope(t *testing.T) {
	type change func(string, int, []map[string]any) []map[string]any
	for _, tc := range []struct {
		name     string
		ordinary bool
		review   func(*ManagedRunningRegistrationScope)
		modify   change
		count    int32
	}{
		{name: "ordinary reader", ordinary: true},
		{name: "wrong controller", review: func(s *ManagedRunningRegistrationScope) { s.ControllerID = registrationRowID }},
		{name: "unknown repository", review: func(s *ManagedRunningRegistrationScope) { s.RepositoryID = registrationForeignRepo }},
		{name: "wrong source", review: func(s *ManagedRunningRegistrationScope) { s.SourceIdentity = strings.Repeat("b", 64) }, count: 1},
		{name: "wrong independently enrolled fingerprint", review: func(s *ManagedRunningRegistrationScope) { s.OwnershipFingerprint = strings.Repeat("b", 64) }, count: 1},
		{name: "pool mapping drifts", count: 4, modify: func(path string, n int, rows []map[string]any) []map[string]any {
			if path == "/api/v1/pools" && n == 2 {
				rows[0]["repo_id"] = registrationForeignRepo
			}
			return rows
		}},
		{name: "unknown repository owner", count: 1, modify: func(path string, _ int, rows []map[string]any) []map[string]any {
			if path == "/api/v1/pools" {
				delete(rows[0], "repo_id")
			}
			return rows
		}},
		{name: "nonrunning row", count: 4, modify: modifyRegistrationRow(func(row map[string]any) { row["status"] = "creating" })},
		{name: "missing generation", count: 4, modify: modifyRegistrationRow(func(row map[string]any) { delete(row, "generation") })},
		{name: "missing agent", count: 4, modify: modifyRegistrationRow(func(row map[string]any) { delete(row, "agent_id") })},
		{name: "missing provider id", count: 4, modify: modifyRegistrationRow(func(row map[string]any) { delete(row, "provider_id") })},
		{name: "scaleset", count: 3, modify: modifyRegistrationRow(func(row map[string]any) { row["scale_set_id"] = 1 })},
		{name: "same agent in approved pool", count: 4, modify: registrationOverlap(managedPoolID, "", false)},
		{name: "same agent with unknown pool", count: 4, modify: registrationOverlap(otherID, "", false)},
		{name: "same provider object in another pool", count: 4, modify: registrationOverlap(otherID, registrationForeignRepo, true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRegistrationFixture(t, tc.ordinary, tc.modify)
			review := f.enrolled
			if tc.review != nil {
				tc.review(&review)
			}
			got, err := f.c.ObserveRunningRegistration(t.Context(), review, registrationRowID)
			if _, _, ok := got.ReportedAgentID(review, registrationRowID); err != ErrManagement || ok || f.gets.Load() != tc.count {
				t.Fatalf("invalid source or scope didn't close: %v %v %v", err, ok, f.gets.Load())
			}
		})
	}
}

func TestManagedRunningRegistrationIgnoresPositiveForeignAgentWithoutExposingRoster(t *testing.T) {
	f := newRegistrationFixture(t, false, registrationOverlap(otherID, registrationForeignRepo, false))
	pos, err := f.c.ObserveRunningRegistration(t.Context(), f.enrolled, registrationRowID)
	if id, _, ok := pos.ReportedAgentID(f.enrolled, registrationRowID); err != nil || !ok || id != registrationAgentID {
		t.Fatal("positively foreign repository changed the requested report", err)
	}
	if _, err := f.c.ObserveRunningRegistration(t.Context(), f.enrolled, registrationOtherRowID); err != ErrManagement {
		t.Fatal("foreign runner exposed by UUID", err)
	}
	if _, err := f.c.ObserveRunningRegistration(context.Background(), f.enrolled, "private-runner-name"); err != ErrManagement || f.gets.Load() != 8 {
		t.Fatal("runner name reached inventory", err)
	}
	o, err := f.c.Observe(t.Context())
	data, marshalErr := json.Marshal(o)
	if err != nil || marshalErr != nil || strings.Contains(string(data), "agent_id") || strings.Contains(string(data), registrationOtherRowID) {
		t.Fatal("standard demand now exposes agent or foreign roster", err)
	}
}

func modifyRegistrationRow(f func(map[string]any)) func(string, int, []map[string]any) []map[string]any {
	return func(path string, _ int, rows []map[string]any) []map[string]any {
		if path == "/api/v1/instances" {
			f(rows[0])
		}
		return rows
	}
}

func registrationOverlap(pool, foreign string, providerOverlap bool) func(string, int, []map[string]any) []map[string]any {
	return func(path string, _ int, rows []map[string]any) []map[string]any {
		if path == "/api/v1/pools" && foreign != "" {
			rows = append(rows, registrationFixturePool(pool, foreign))
		}
		if path == "/api/v1/instances" {
			providerID := "other-provider-object"
			if providerOverlap {
				providerID = "private-provider-object"
			}
			rows = append(rows, registrationFixtureInstance(registrationOtherRowID, pool, "other-runner", providerID))
		}
		return rows
	}
}

func TestManagedRunningRegistrationOwnerCloseDoesNotChangeOrdinaryReads(t *testing.T) {
	f := newRegistrationFixture(t, false, nil)
	report, err := f.c.ObserveRunningRegistration(t.Context(), f.enrolled, registrationRowID)
	if err != nil {
		t.Fatal(err)
	}
	alias := *f.c
	alias.Close()
	if id, at, ok := report.ReportedAgentID(f.enrolled, registrationRowID); ok || id != 0 || !at.IsZero() {
		t.Fatal("closed local owner did not retire existing sample")
	}
	if _, err := f.c.ObserveRunningRegistration(t.Context(), f.enrolled, registrationRowID); err != ErrManagement || f.gets.Load() != 4 {
		t.Fatal("closed owner permitted a new report or request", err)
	}
	if _, err := f.c.Observe(t.Context()); err != nil || f.gets.Load() != 8 {
		t.Fatal("report lifetime changed legacy ordinary use after Close", err)
	}
	fresh, err := f.reopen().ObserveRunningRegistration(t.Context(), f.enrolled, registrationRowID)
	if id, _, ok := fresh.ReportedAgentID(f.enrolled, registrationRowID); err != nil || !ok || id != registrationAgentID {
		t.Fatal("new exact owner could not issue an independent report", err)
	}
	if _, _, ok := report.ReportedAgentID(f.enrolled, registrationRowID); ok {
		t.Fatal("new owner reopened an old report")
	}
}

func TestManagedRunningRegistrationCloseDuringCaptureRefusesIssue(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	f := newRegistrationFixture(t, false, func(path string, n int, rows []map[string]any) []map[string]any {
		if path == "/api/v1/pools" && n == 2 {
			close(entered)
			<-release
		}
		return rows
	})
	done := make(chan error, 1)
	go func() {
		r, err := f.c.ObserveRunningRegistration(t.Context(), f.enrolled, registrationRowID)
		if _, _, ok := r.ReportedAgentID(f.enrolled, registrationRowID); ok {
			t.Error("close during capture returned a readable sample")
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("report did not reach the actual final pool read")
	}
	f.c.Close()
	close(release)
	select {
	case err := <-done:
		if err != ErrManagement || f.gets.Load() != 4 {
			t.Fatal("in-flight legacy reads minted a report after local close", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("report did not return")
	}
}
