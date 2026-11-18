package adminview

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/jobobserver"
	"testing"
)

func TestJobObserverStatusRequiresReadAuthorizationAndNeverRelease(t *testing.T) {
	h, _, token := fixture(t)
	var missing *jobobserver.Service
	h.SetJobObserver(missing)
	for _, v := range []struct {
		method, token string
		code          int
	}{{"GET", "", 401}, {"POST", token, 405}, {"DELETE", token, 405}} {
		w := request(t, h, v.method, "/admin/api/v1/job-observer-status", v.token)
		if w.Code != v.code {
			t.Fatal(w.Code)
		}
	}
	w := request(t, h, "GET", "/admin/api/v1/job-observer-status", token)
	var got struct {
		State   string             `json:"state"`
		Release bool               `json:"release_authorized"`
		Status  jobobserver.Status `json:"status"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.State != "unconfigured" || got.Release || !got.Status.At.IsZero() {
		t.Fatal(w.Code, w.Body.String())
	}
}
