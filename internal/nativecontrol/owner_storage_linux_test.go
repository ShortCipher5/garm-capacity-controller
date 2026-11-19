//go:build linux

package nativecontrol

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStorageAccountingRejectsMissingOriginalEvidence(t *testing.T) {
	if _, e := storageReport(nativeapi.StorageReclamationObservation{}, strings.Repeat("a", 64), strings.Repeat("b", 64), "id"); e == nil {
		t.Fatal("fabricated accounting")
	}
	owner := &executionOwner{}
	for _, x := range []*ownerExecution{nil, {}, {retirement: &ownerRetirement{}, reclamationCaptured: true}} {
		if _, e := owner.retirementStorageAccounting(context.Background(), x, &ownerRetirementStep{}); e == nil {
			t.Fatal("missing actual capabilities accepted")
		}
	}
}
func TestStorageAccountingRouteDoesNotInferFromDeleteSuccess(t *testing.T) {
	claim := strings.Repeat("a", 64)
	step := &ownerRetirementStep{}
	step.claim.ID = claim
	step.current.Outcome = "succeeded"
	x := &ownerExecution{retirement: &ownerRetirement{deleted: step}}
	owner := &executionOwner{retirementSteps: map[string]*ownerExecution{claim: x}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/native/retirement-operations/"+claim+"/storage-accounting", nil)
	if !owner.serveRetirementOperation(w, r) || w.Code != 409 || x.retirement.accounting != nil {
		t.Fatal("DELETE receipt became physical proof", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest("POST", "/v1/native/retirement-operations/"+claim+"/storage-accounting", nil)
	if !owner.serveRetirementOperation(w, r) || w.Code != 400 {
		t.Fatal("write method allowed", w.Code)
	}
}
