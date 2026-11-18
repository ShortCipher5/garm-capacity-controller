//go:build linux

package nativezfs

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

type sealMock struct {
	props          map[string]map[string]string
	calls          [][]string
	consumed       bool
	failAt         int
	inherited      bool
	inheritedQuota bool
}

func (m *sealMock) run(_ context.Context, a []string, _ *os.File) ([]byte, error) {
	m.calls = append(m.calls, append([]string(nil), a...))
	if len(m.calls) == m.failAt {
		return nil, ErrUnknown
	}
	if a[0] == "get" {
		p := m.props[a[len(a)-1]]
		if p == nil {
			return nil, ErrRefused
		}
		if a[4] == "value,source" {
			if m.inherited || (a[5] == "quota" && m.inheritedQuota) {
				return []byte(p[a[5]] + "\tinherited from tank/root\n"), nil
			}
			return []byte(p[a[5]] + "\tlocal\n"), nil
		}
		var out strings.Builder
		for _, k := range properties {
			fmt.Fprintf(&out, "%s\t%s\t%s\n", a[len(a)-1], k, p[k])
		}
		return []byte(out.String()), nil
	}
	if !m.consumed {
		panic("effect before durable consume")
	}
	if a[0] == "set" {
		v := strings.SplitN(a[1], "=", 2)
		m.props[a[2]][v[0]] = v[1]
	}
	return nil, nil
}
func sealFixture(t *testing.T) (sealRecord, *sealMock, *os.File) {
	_, id, ns := testInputs(t)
	r := Record{CapsuleID: id, RootDataset: "tank/root", RootGUID: "1", Dataset: "tank/root/domain-" + id, GUID: "2", RuntimeGUID: "3", PoolGUID: "4", QuotaBytes: 1000}
	b := CategoryBudgets{Runtime: 100, Categories: map[string]int64{}}
	for _, n := range sealedCategories {
		b.Categories[n] = 50
	}
	roots, e := budgetRoots(r, b)
	if e != nil {
		t.Fatal(e)
	}
	m := &sealMock{props: map[string]map[string]string{}}
	add := func(n, g, q, z, mp, cm string) {
		m.props[n] = map[string]string{"guid": g, "quota": q, "zoned": z, "type": "filesystem", "mountpoint": mp, "canmount": cm}
	}
	add(r.RootDataset, "1", "0", "off", "none", "off")
	add(r.Dataset, "2", "1000", "on", "none", "off")
	add(r.Dataset+"/runtime", "3", "0", "on", "legacy", "noauto")
	add(r.Dataset+"/pool", "4", "0", "on", "legacy", "noauto")
	for i, v := range roots[1:] {
		add(v.Name, strconv.Itoa(i+5), "0", "on", "legacy", "noauto")
	}
	return sealRecord{Version: 1, Birth: r, Roots: roots}, m, ns
}
func TestSealRemovesBroadAuthorityBeforeLeafDelegation(t *testing.T) {
	r, m, ns := sealFixture(t)
	got, e := seal(context.Background(), r, ns, m, func(sealRecord) error { m.consumed = true; return nil })
	if e != nil {
		t.Fatal(e)
	}
	unzone := -1
	zones := 0
	for i, a := range m.calls {
		if a[0] == "unzone" {
			unzone = i
			if a[2] != r.Birth.Dataset {
				t.Fatal(a)
			}
		}
		if a[0] == "zone" {
			zones++
			if unzone < 0 || i <= unzone || a[2] == r.Birth.Dataset || a[2] == r.Birth.Dataset+"/pool" || strings.HasSuffix(a[2], "/deleted") {
				t.Fatal(a)
			}
		}
	}
	if zones != 11 {
		t.Fatal(zones)
	}
	if checkSeal(context.Background(), got, ns, m) != nil {
		t.Fatal("readback")
	}
	m.props[got.Roots[2].Name]["guid"] = "999"
	if checkSeal(context.Background(), got, ns, m) == nil {
		t.Fatal("accepted replacement")
	}
}
func TestSealRejectsEveryPartialEffectWithoutCleanup(t *testing.T) {
	r, m, ns := sealFixture(t)
	_, e := seal(context.Background(), r, ns, m, func(sealRecord) error { m.consumed = true; return nil })
	if e != nil {
		t.Fatal(e)
	}
	total := len(m.calls)
	for stop := 1; stop <= total; stop++ {
		r, m, ns := sealFixture(t)
		m.failAt = stop
		_, e := seal(context.Background(), r, ns, m, func(sealRecord) error { m.consumed = true; return nil })
		if e == nil {
			t.Fatalf("call %d accepted", stop)
		}
		for _, a := range m.calls {
			if a[0] == "destroy" {
				t.Fatal("cleanup")
			}
		}
	}
}
func TestSealRequiresLocalSetpointAndDurableIntent(t *testing.T) {
	r, m, ns := sealFixture(t)
	_, e := seal(context.Background(), r, ns, m, func(sealRecord) error { return ErrUnknown })
	if e == nil {
		t.Fatal("intent")
	}
	for _, a := range m.calls {
		if a[0] != "get" {
			t.Fatal("write")
		}
	}
	r, m, ns = sealFixture(t)
	m.inherited = true
	_, e = seal(context.Background(), r, ns, m, func(sealRecord) error { m.consumed = true; return nil })
	if e == nil {
		t.Fatal("inherited quota bypass")
	}
}
func TestBudgetLimitsAndCopies(t *testing.T) {
	r, _, _ := sealFixture(t)
	s := SealedBudgets{record: r}
	b := s.Budgets()
	b.Categories["images"] = 999
	if s.Budgets().Categories["images"] == 999 {
		t.Fatal("shared map")
	}
	b = s.Budgets()
	b.Runtime = 1001
	if _, e := budgetRoots(r.Birth, b); e == nil {
		t.Fatal("overcommit")
	}
	b = s.Budgets()
	delete(b.Categories, "deleted/images")
	if _, e := budgetRoots(r.Birth, b); e == nil {
		t.Fatal("missing retained image budget")
	}
	if (SealedBudgets{}).Recheck(context.Background()) == nil {
		t.Fatal("fabrication")
	}
}
func TestSealHelperGrammar(t *testing.T) {
	for _, a := range [][]string{{"set", "quota=0", "tank/root"}, {"set", "quota=-1", "tank/root"}, {"set", "quota=01", "tank/root"}, {"unzone", "/proc/1/ns/user", "tank/root"}, {"destroy", "tank/root"}} {
		if validCommand(a) {
			t.Fatal(a)
		}
	}
	for _, a := range [][]string{{"set", "quota=1", "tank/root/domain-" + strings.Repeat("a", 64) + "/runtime"}, {"unzone", "/proc/self/fd/4", "tank/root/domain-" + strings.Repeat("a", 64)}, {"get", "-H", "-p", "-o", "value,source", "zoned", "tank/root"}} {
		if !validCommand(a) {
			t.Fatal(a)
		}
	}
}

func TestQuotaSourceMustBeLocal(t *testing.T) {
	r, m, ns := sealFixture(t)
	m.inheritedQuota = true
	_, e := seal(context.Background(), r, ns, m, func(sealRecord) error { m.consumed = true; return nil })
	if e == nil {
		t.Fatal("nonlocal quota accepted")
	}
}
func TestFailedSealRecheckPermanentlyPoisonsCopies(t *testing.T) {
	state := &sealState{}
	a := SealedBudgets{state: state}
	b := a
	if a.Recheck(context.Background()) == nil {
		t.Fatal("invalid handle accepted")
	}
	called := false
	if e := b.state.check(func() error { called = true; return nil }); e == nil || called {
		t.Fatal("copied handle revived")
	}
	if e := state.check(func() error { return nil }); e == nil {
		t.Fatal("failure reset")
	}
}

func TestConfigurationBudgetValidationNeedsNoBirth(t *testing.T) {
	b := CategoryBudgets{Runtime: 1, Categories: map[string]int64{}}
	for _, n := range sealedCategories {
		b.Categories[n] = 1
	}
	if ValidateCategoryBudgets(11, b) != nil {
		t.Fatal("exact total rejected")
	}
	for _, total := range []int64{-1, 0, 10} {
		if ValidateCategoryBudgets(total, b) == nil {
			t.Fatal("invalid total", total)
		}
	}
	b.Categories["unknown"] = 1
	delete(b.Categories, "images")
	if ValidateCategoryBudgets(100, b) == nil {
		t.Fatal("unknown replaces required category")
	}
}
