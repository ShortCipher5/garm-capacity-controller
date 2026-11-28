package adminview

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incussetup"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIncusSetupReviewedAPIAndJournal(t *testing.T) {
	h, store, read := fixture(t)
	ctx := context.Background()
	if err := store.InitializeIncusSetup(ctx); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("b", 64)
	path := "/admin/api/v1/incus-setup"
	send := func(method, body, cred, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://127.0.0.1"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+cred)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	if send("GET", "", token, "").Code != 404 {
		t.Fatal("must be disabled by default")
	}
	b := bootstrapFixture(t).Plan
	target := incussetup.Target{Address: b.Address, Port: b.Port, User: b.AdminUser, HostKey: b.HostKey, AdminFingerprint: b.AdminKeyFingerprint}
	no := false
	finish := make(chan struct{})
	started := make(chan struct{})
	targetID := "server1"
	targets := map[string]IncusSetupTarget{targetID: {Target: target, Execute: func(ctx context.Context, p *incussetup.Report) (incussetup.Report, error) {
		if p == nil {
			return incussetup.Report{Nonce: strings.Repeat("1", 64), State: "missing", Version: "6.0.4+deb13u1", Outcome: "observed", Eligible: &no}, nil
		}
		a, e := store.LatestIncusSetup(ctx, targetID)
		if e != nil || a.Status != "unknown" || a.Digest != target.Review(*p) {
			t.Errorf("missing intent: %+v %v", a, e)
		}
		close(started)
		select {
		case <-finish:
		case <-ctx.Done():
			return incussetup.Report{}, ctx.Err()
		}
		return incussetup.Report{State: "configured", Outcome: "verified", Eligible: &no}, nil
	}}}
	if _, e := h.SetIncusSetup(targets, read); e == nil {
		t.Fatal("read role accepted")
	}
	stop, e := h.SetIncusSetup(targets, token)
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	if send("GET", "", read, "").Code != 401 || send("GET", "", token, "https://other.invalid").Code != 403 {
		t.Fatal("role/origin")
	}
	if send("POST", `{"target_id":"server1","action":"inspect","command":"custom"}`, token, "").Code != 400 {
		t.Fatal("custom input accepted")
	}
	obs := send("POST", `{"target_id":"server1","action":"inspect"}`, token, "")
	var reply struct {
		Digest string `json:"review_digest"`
	}
	if obs.Code != 200 || json.Unmarshal(obs.Body.Bytes(), &reply) != nil || len(reply.Digest) != 64 {
		t.Fatal(obs.Code, obs.Body.String())
	}
	raw := `{"target_id":"server1","action":"apply","confirm_install":true,"review_digest":"` + reply.Digest + `"}`
	if send("POST", strings.Replace(raw, reply.Digest, strings.Repeat("0", 64), 1), token, "").Code != 409 {
		t.Fatal("wrong digest accepted")
	}
	if w := send("POST", raw, token, ""); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	<-started
	if send("POST", raw, token, "").Code != 409 {
		t.Fatal("review replay")
	}
	close(finish)
	until := time.Now().Add(3 * time.Second)
	for {
		a, e := store.LatestIncusSetup(ctx, targetID)
		if e != nil {
			t.Fatal(e)
		}
		if a.Status == "verified" {
			break
		}
		if time.Now().After(until) {
			t.Fatal(a)
		}
		time.Sleep(time.Millisecond)
	}
	if send("POST", raw, token, "").Code != 409 {
		t.Fatal("consumed review replay")
	}
}
