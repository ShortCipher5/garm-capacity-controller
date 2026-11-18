//go:build linux

package nativequota

import (
	"context"
	"testing"
	"time"
)

func TestIdentityInventoryRetainsRenamedAndReferencedDatasets(t *testing.T) {
	root := FilesystemIdentity{"tank/domain", "1", 100}
	original := Observation{"tank/domain/pool/guest", "42", 10, time.Now()}
	base := "tank/domain\t1\tfilesystem\t-\t-\t-\n"
	for _, row := range []string{"tank/domain/renamed\t42\tfilesystem\t-\t-\t-\n", "tank/domain/pool/guest\t43\tfilesystem\t-\t-\t-\n", "tank/domain/pool/guest@snap\t44\tsnapshot\t-\ton\t2\n", "tank/domain/pool/guest#book\t44\tbookmark\t-\t-\t-\n", "tank/domain/clone\t45\tfilesystem\ttank/domain/pool/guest@snap\t-\t-\n"} {
		items, e := parseIdentityInventory([]byte(base+row), root)
		if e != nil {
			t.Fatal(e, row)
		}
		if identityGone(items, original) {
			t.Fatal("retained identity disappeared", row)
		}
	}
	items, e := parseIdentityInventory([]byte(base), root)
	if e != nil || !identityGone(items, original) {
		t.Fatal(e)
	}
}
func TestIdentityInventoryRejectsPartialAndScopeDrift(t *testing.T) {
	root := FilesystemIdentity{"tank/domain", "1", 100}
	for _, raw := range []string{"", "tank/domain\t1\tfilesystem\t-\t-\t-", "tank/domain\t2\tfilesystem\t-\t-\t-\n", "tank/other\t1\tfilesystem\t-\t-\t-\n", "tank/domain\t1\tfilesystem\t-\t-\t-\ntank/domain\t1\tfilesystem\t-\t-\t-\n", "tank/domain\t1\tfilesystem\t-\t-\t-\ntank/domain/x\t2\tother\t-\t-\t-\n"} {
		if _, e := parseIdentityInventory([]byte(raw), root); e == nil {
			t.Fatal(raw)
		}
	}
	var l DatasetLifetime
	if _, e := l.ObserveIdentityAbsence(context.Background()); e == nil {
		t.Fatal("fabricated lifetime")
	}
	var r *Reader
	if _, e := r.CaptureLifetime(context.Background(), root, Observation{}); e == nil {
		t.Fatal("fabricated capture")
	}
}
