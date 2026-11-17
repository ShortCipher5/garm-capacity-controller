package main

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"testing"
)

func TestAssessRecoveryCLI(t *testing.T) {
	path, store := fixture(t)
	if _, err := store.Admit(context.Background(), request("pending")); err != nil {
		t.Fatal(err)
	}
	code, out, diagnostic := invoke(context.Background(), "assess-recovery", "--source", path, "--limit", "1")
	var report admission.RecoveryAssessment
	if code != 0 || diagnostic != "" || json.Unmarshal([]byte(out), &report) != nil || report.ResumeAuthorized || len(report.Entries) != 1 {
		t.Fatalf("assessment %d %q %q", code, out, diagnostic)
	}
	for _, extra := range []string{"--resume", "--repair", "--old-writers-fenced"} {
		if code, _, _ := invoke(context.Background(), "assess-recovery", "--source", path, extra); code != 2 {
			t.Fatal("unexpected mutation/authority flag accepted")
		}
	}
}
