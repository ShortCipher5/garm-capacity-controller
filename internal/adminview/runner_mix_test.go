package adminview

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"testing"
)

func TestRunnerMixReviewApplyAuthAndConflict(t *testing.T) {
	h, read, write := managementFixture(t)
	path := "/admin/api/v1/runner-mix"
	if r := request(t, h, "GET", path+"/host-a", ""); r.Code != 401 {
		t.Fatal(r.Code)
	}
	r := request(t, h, "GET", path+"/host-a", read)
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	var v admission.RunnerMixView
	if json.Unmarshal(r.Body.Bytes(), &v) != nil {
		t.Fatal(r.Body.String())
	}
	m := admission.RunnerMix{Host: "host-a", Entries: []admission.RunnerMixEntry{{Name: "small", CPU: 1000, RAM: 1024, Disk: 2048, Count: 2}}}
	b, _ := json.Marshal(map[string]any{"expected_sequence": v.Sequence, "mix": m})
	if r = post(t, h, "POST", path, read, string(b)); r.Code != 401 {
		t.Fatal("read token write", r.Code)
	}
	if r = post(t, h, "POST", path, write, string(b)); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r = post(t, h, "POST", path, write, string(b)); r.Code != 409 {
		t.Fatal("replayed", r.Code, r.Body.String())
	}
	r = request(t, h, "GET", path+"/host-a", read)
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &v) != nil || !v.ProfilesMatch || v.Mix.Revision != 1 {
		t.Fatal(r.Body.String())
	}
}
