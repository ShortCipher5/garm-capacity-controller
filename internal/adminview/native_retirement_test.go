package adminview

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecontroller"
	"testing"
)

func TestNativeRetirementStatusIsAuthenticatedReadOnlyAndNoRelease(t *testing.T) {
	h, _, token := fixture(t)
	h.SetNativeRetirement(nil)
	for _, v := range []struct {
		method, token string
		code          int
	}{{"GET", "", 401}, {"POST", token, 405}, {"DELETE", token, 405}} {
		w := request(t, h, v.method, "/admin/api/v1/native-retirement-status", v.token)
		if w.Code != v.code {
			t.Fatal(w.Code)
		}
	}
	w := request(t, h, "GET", "/admin/api/v1/native-retirement-status", token)
	var out struct {
		State   string                            `json:"state"`
		Release bool                              `json:"release_authorized"`
		Status  nativecontroller.RetirementStatus `json:"status"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.State != "unconfigured" || out.Release || !out.Status.At.IsZero() {
		t.Fatal(w.Code, w.Body.String())
	}
}
