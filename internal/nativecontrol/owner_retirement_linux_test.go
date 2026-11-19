//go:build linux

package nativecontrol

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRetirementCannotAdoptAbsentOwner(t *testing.T) {
	o := &executionOwner{permits: map[string]*ownerExecution{}}
	if _, e := o.retire(context.Background(), admission.NativeRetirementIntent{PermitID: strings.Repeat("a", 32)}, admission.NativeRetireStop); e == nil {
		t.Fatal("adopted missing incarnation")
	}
	if retirementMatches(&ownerExecution{}, admission.NativeRetirementIntent{}) {
		t.Fatal("empty execution accepted")
	}
}
func TestRetirementRoutesRefuseMalformedOrUnknown(t *testing.T) {
	o := &executionOwner{}
	for _, path := range []string{"/v1/native/executions/abc/retire-stop", "/v1/native/retirement-operations/abc", "/v1/native/retirement-operations/" + strings.Repeat("a", 64)} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		if !o.serveRetirementOperation(w, r) || w.Code < 400 {
			t.Fatalf("unexpected response %s %d", path, w.Code)
		}
	}
}
