//go:build linux

package nativezfs

import (
	"context"
	"errors"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capsulepeer"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type mockCommands struct {
	calls [][]string
	fail  int
	drift bool
}

func (m *mockCommands) run(_ context.Context, args []string, _ *os.File) ([]byte, error) {
	m.calls = append(m.calls, append([]string(nil), args...))
	if len(m.calls) == m.fail {
		return nil, errors.New("unknown")
	}
	if args[0] != "get" {
		return nil, nil
	}
	name := args[len(args)-1]
	p := map[string]string{"guid": "1", "type": "filesystem", "quota": "0", "zoned": "off", "mountpoint": "none", "canmount": "off"}
	if strings.Contains(name, "/domain-") {
		p["guid"] = "2"
		p["quota"] = "4096"
		p["zoned"] = "off"
		for _, a := range m.calls {
			if a[0] == "set" {
				p["zoned"] = "on"
			}
		}
	}
	if strings.HasSuffix(name, "/runtime") {
		p["guid"] = "3"
		p["quota"] = "0"
		p["mountpoint"] = "legacy"
		p["canmount"] = "noauto"
	}
	if strings.HasSuffix(name, "/pool") {
		p["guid"] = "4"
		p["quota"] = "0"
		p["mountpoint"] = "legacy"
		p["canmount"] = "noauto"
	}
	if m.drift && len(m.calls) > 8 {
		p["quota"] = "8192"
	}
	var out strings.Builder
	for _, k := range properties {
		fmt.Fprintf(&out, "%s\t%s\t%s\n", name, k, p[k])
	}
	return []byte(out.String()), nil
}

type mockJournal struct {
	consumed  bool
	fail      bool
	completed bool
}

func (j *mockJournal) consume(Record) error {
	if j.consumed || j.fail {
		return ErrUnknown
	}
	j.consumed = true
	return nil
}
func (j *mockJournal) complete(Record) error { j.completed = true; return nil }
func testInputs(t *testing.T) (Configuration, string, *os.File) {
	t.Helper()
	f, e := os.CreateTemp(t.TempDir(), "namespace-fixture")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.Close() })
	return Configuration{RootDataset: "tank/trusted", RootGUID: "1", QuotaBytes: 4096}, strings.Repeat("a", 64), f
}
func TestFreshSequenceExactAndConsumedBeforeEffects(t *testing.T) {
	c, id, ns := testInputs(t)
	m := &mockCommands{}
	j := &mockJournal{}
	r, e := provision(context.Background(), c, id, 42, ns, m, j, 100000, 100000, func(Record) error { return nil })
	if e != nil || !j.consumed || r.GUID != "2" || r.RuntimeGUID != "3" || r.PoolGUID != "4" || r.UserNamespace != 42 {
		t.Fatalf("%+v %v", r, e)
	}
	if j.completed {
		t.Fatal("engine cannot mark complete before peer recheck")
	}
	if len(m.calls) != 11 {
		t.Fatal(m.calls)
	}
	if strings.Join(m.calls[8], " ") != "zone /proc/self/fd/4 tank/trusted/domain-"+id {
		t.Fatal(m.calls)
	}
	for _, call := range m.calls {
		for _, arg := range call {
			if arg == "-p" && call[0] == "create" || arg == "destroy" || arg == "unzone" {
				t.Fatal("unsafe mutation", call)
			}
		}
	}
	before := len(m.calls)
	if _, e = provision(context.Background(), c, id, 42, ns, m, j, 100000, 100000, func(Record) error { return nil }); e == nil || len(m.calls) != before+1 {
		t.Fatal("replayed effects")
	}
}
func TestEveryFailureQuarantinesWithoutCleanup(t *testing.T) {
	for i := 1; i <= 11; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			c, id, ns := testInputs(t)
			m := &mockCommands{fail: i}
			j := &mockJournal{}
			_, e := provision(context.Background(), c, id, 42, ns, m, j, 100000, 100000, func(Record) error { return nil })
			if e == nil || len(m.calls) != i {
				t.Fatal(e, m.calls)
			}
			if i > 1 && (!j.consumed || e != ErrUnknown) {
				t.Fatal("lost uncertainty")
			}
			for _, call := range m.calls {
				if call[0] == "destroy" || call[0] == "unzone" {
					t.Fatal("cleanup after uncertainty")
				}
			}
		})
	}
}
func TestRootDriftJournalFailureAndParentQuotaDrift(t *testing.T) {
	c, id, ns := testInputs(t)
	c.RootGUID = "999"
	m := &mockCommands{}
	j := &mockJournal{}
	if _, e := provision(context.Background(), c, id, 42, ns, m, j, 100000, 100000, func(Record) error { return nil }); e == nil || j.consumed || len(m.calls) != 1 {
		t.Fatal("adopted root")
	}
	c.RootGUID = "1"
	m = &mockCommands{}
	j = &mockJournal{fail: true}
	if _, e := provision(context.Background(), c, id, 42, ns, m, j, 100000, 100000, func(Record) error { return nil }); e != ErrUnknown || len(m.calls) != 1 {
		t.Fatal("effect before durable journal")
	}
	m = &mockCommands{drift: true}
	if _, e := provision(context.Background(), c, id, 42, ns, m, &mockJournal{}, 100000, 100000, func(Record) error { return nil }); e != ErrUnknown {
		t.Fatal("quota drift accepted")
	}
}
func TestNoCallerConstructedNamespaceAuthority(t *testing.T) {
	for _, peer := range []*capsulepeer.Peer{nil, {}} {
		if _, e := CreateFresh(context.Background(), peer, Configuration{}); e == nil {
			t.Fatal("zero peer accepted")
		}
	}
	for _, p := range []string{"/dev/sda", "tank/x/../y", "tank/x@snap", "tank/x;evil", "tank/x,y", "tank/x\n", "tank//x"} {
		if dataset(p) {
			t.Fatal(p)
		}
	}
	if supportedVersion([]byte("zfs-2.2.6\nzfs-kmod-2.3.0\n")) {
		t.Fatal("unreviewed kernel")
	}
}
func TestDurableJournalNeverAdoptsExistingAttempt(t *testing.T) {
	dir := t.TempDir()
	j := fileJournal{filepath.Join(dir, "birth"), strings.Repeat("a", 64)}
	r := Record{CapsuleID: j.id}
	if j.consume(r) != nil {
		t.Fatal("consume")
	}
	if j.consume(r) == nil {
		t.Fatal("adopted attempt")
	}
	if j.complete(r) != nil || j.complete(r) == nil {
		t.Fatal("completion overwrote")
	}
	raw, e := os.ReadFile(filepath.Join(j.directory, "attempt.json"))
	if e != nil || string(raw) != string(recordBytes(r)) {
		t.Fatal("receipt mismatch")
	}
}
func TestStrictBoundedPropertyReadback(t *testing.T) {
	c, id, ns := testInputs(t)
	_ = id
	m := &mockCommands{}
	raw, _ := m.run(context.Background(), []string{"get", c.RootDataset}, ns)
	if _, e := parseProperties(raw, c.RootDataset); e != nil {
		t.Fatal(e)
	}
	for _, bad := range [][]byte{append(raw, raw...), []byte(strings.Replace(string(raw), "tank/trusted", "tank/other", 1)), []byte(strings.Replace(string(raw), "\tguid\t1", "\tguid\t", 1)), raw[:len(raw)-1], []byte(strings.Repeat("x", 8193))} {
		if _, e := parseProperties(bad, c.RootDataset); e == nil {
			t.Fatal("ambiguous output")
		}
	}
}

func TestRunningBirthRecheckRejectsQuotaDriftAndZeroHandle(t *testing.T) {
	c, id, ns := testInputs(t)
	m := &mockCommands{}
	r, e := provision(context.Background(), c, id, 42, ns, m, &mockJournal{}, 100000, 100000, func(Record) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	if e = checkBirth(context.Background(), r, ns, m); e != nil {
		t.Fatal(e)
	}
	m.drift = true
	if checkBirth(context.Background(), r, ns, m) == nil {
		t.Fatal("changed quota accepted")
	}
	if (Birth{}).Recheck(context.Background()) == nil {
		t.Fatal("record-only authority")
	}
}
