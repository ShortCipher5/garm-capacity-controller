//go:build linux

package nativequota

import (
	"strings"
	"testing"
	"time"
)

func TestExactQuotaParsing(t *testing.T) {
	raw := "tank/containers/runner\tguid\t1234567\ntank/containers/runner\ttype\tfilesystem\ntank/containers/runner\tquota\t1048576\n"
	o, e := parse([]byte(raw), "tank/containers/runner", 1048576, time.Now())
	if e != nil || !o.Valid() || o.GUID() != "1234567" || o.Bytes() != 1048576 {
		t.Fatal(o, e)
	}
	for _, bad := range []string{strings.Replace(raw, "1048576", "none", 1), strings.Replace(raw, "1048576", "1M", 1), strings.Replace(raw, "1048576", "0", 1), strings.Replace(raw, "1048576", "2097152", 1), strings.Replace(raw, "1234567", "0", 1), strings.Replace(raw, "1234567", "00123", 1), strings.Replace(raw, "filesystem", "volume", 1), strings.Replace(raw, "quota", "refquota", 1), strings.Replace(raw, "guid", "quota", 1), strings.Replace(raw, "tank/containers/runner", "tank/containers/other", 1), raw + "extra\n", raw + "\n"} {
		if _, e := parse([]byte(bad), "tank/containers/runner", 1048576, time.Now()); e == nil {
			t.Fatal("accepted", bad)
		}
	}
}
func TestDatasetRejectsOptionAndSnapshotInjection(t *testing.T) {
	for _, s := range []string{"", "tank", "-r/tank", "tank/../runner", "tank//runner", "tank/runner@snap", "tank/runner#bookmark", "tank/runner\n", "tank/runner\t", "tank/runner;id"} {
		if validDataset(s) {
			t.Fatal(s)
		}
	}
}
func TestZeroObservationAndReaderRefuse(t *testing.T) {
	if (Observation{}).Valid() {
		t.Fatal("zero proof")
	}
	var r *Reader
	if _, e := r.Read(t.Context(), "tank/runner", 1); e == nil {
		t.Fatal("nil reader")
	}
}

func TestFilesystemIdentityDoesNotGrantQuota(t *testing.T) {
	raw := []byte("tank/birth/runtime\tguid\t123\ntank/birth/runtime\ttype\tfilesystem\ntank/birth/runtime\tquota\t0\n")
	v, e := parseFilesystem(raw, "tank/birth/runtime")
	if e != nil || v.GUID() != "123" || v.Quota() != 0 {
		t.Fatal(v, e)
	}
	if _, e := parse(raw, "tank/birth/runtime", 0, time.Now()); e == nil {
		t.Fatal("zero quota granted")
	}
	for _, bad := range []string{"none", "-1", "00"} {
		r := strings.Replace(string(raw), "quota\t0", "quota\t"+bad, 1)
		if _, e := parseFilesystem([]byte(r), "tank/birth/runtime"); e == nil {
			t.Fatal(bad)
		}
	}
}
