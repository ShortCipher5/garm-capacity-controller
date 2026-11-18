//go:build linux

package nativequota

import (
	"context"
	"strings"
	"testing"
	"time"
)

func poolFixture() string {
	return "tank\tguid\t1\ntank\tload_guid\t2\ntank\thealth\tONLINE\ntank\treadonly\toff\ntank\tfreeing\t0\ntank\tleaked\t0\ntank\tallocated\t30\ntank\tfree\t70\ntank\tsize\t100\n"
}
func TestPoolAccountingFailClosed(t *testing.T) {
	o, e := parsePool([]byte(poolFixture()), "tank")
	if e != nil || o.GUID() != "1" || o.Free() != 70 {
		t.Fatal(o, e)
	}
	for _, pair := range [][2]string{{"ONLINE", "DEGRADED"}, {"readonly\toff", "readonly\ton"}, {"leaked\t0", "leaked\t1"}, {"guid\t1", "guid\t0"}, {"size\t100", "size\t20"}, {"free\t70", "free\t+70"}} {
		if _, e := parsePool([]byte(strings.Replace(poolFixture(), pair[0], pair[1], 1)), "tank"); e == nil {
			t.Fatal(pair)
		}
	}
	changed := o
	changed.loadGUID = "3"
	if samePool(o, changed) {
		t.Fatal("reimport")
	}
	var r *PoolReader
	if _, e := r.Read(context.Background(), "tank"); e == nil {
		t.Fatal("zero reader")
	}
}
func TestReclamationRejectsRetainedAndNewReferences(t *testing.T) {
	original := Observation{"tank/domain/guest", "42", 10, time.Now()}
	baseline := inventoryIdentity{"tank/image@base", "9", "snapshot", "-", "off", "0"}
	tracker := ReclamationTracker{life: DatasetLifetime{original: original}, targets: map[string]bool{"42": true, "43": true}, references: map[string]inventoryIdentity{baseline.name: baseline}}
	if !tracker.referencesGone([]inventoryIdentity{baseline}) {
		t.Fatal("unchanged baseline")
	}
	for _, row := range []inventoryIdentity{{"tank/outside/renamed", "42", "filesystem", "-", "-", "-"}, {"tank/outside@snap", "43", "snapshot", "-", "off", "0"}, {"tank/new@snap", "44", "snapshot", "-", "off", "0"}, {"tank/newclone", "45", "filesystem", "tank/image@base", "-", "-"}, {"tank/image@base", "9", "snapshot", "-", "on", "0"}, {"tank/image@base", "9", "snapshot", "-", "off", "1"}} {
		if tracker.referencesGone([]inventoryIdentity{baseline, row}) {
			t.Fatal(row)
		}
	}
	var zero ReclamationTracker
	if _, e := zero.ObserveReclamation(context.Background()); e == nil {
		t.Fatal("fabricated")
	}
}
func TestFullPoolInventoryIncludesRootAndAllReferenceKinds(t *testing.T) {
	raw := "tank\t1\tfilesystem\t-\t-\t-\ntank/image@base\t2\tsnapshot\t-\toff\t0\ntank/image#book\t2\tbookmark\t-\t-\t-\n"
	rows, e := parseIdentityInventory([]byte(raw), FilesystemIdentity{dataset: "tank"})
	if e != nil || len(rows) != 3 {
		t.Fatal(rows, e)
	}
	if _, e := parseIdentityInventory([]byte(raw), FilesystemIdentity{dataset: "tank", guid: "3"}); e == nil {
		t.Fatal("rootguid")
	}
}
