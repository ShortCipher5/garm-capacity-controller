package profilesnapshot

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"strings"
	"testing"
	"time"
)

func TestSnapshotNonceHashAndFreshness(t *testing.T) {
	now := time.Now().UTC()
	nonce := strings.Repeat("a", 64)
	lineage := strings.Repeat("b", 64)
	state := admission.BrokerGrantState{Sequence: 12, PoolProfiles: map[string]admission.BrokerPoolProfile{}, HostBindings: map[string]admission.HostBinding{}}
	s, err := New("controller", nonce, lineage, state, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s)
	if _, err = Decode(raw, "controller", nonce, now); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"nonce", "controller", "stale", "future", "hash", "lineage", "duplicate", "alias"} {
		t.Run(mode, func(t *testing.T) {
			v := s
			switch mode {
			case "nonce":
				v.Nonce = strings.Repeat("c", 64)
			case "controller":
				v.Controller = "other"
			case "stale":
				v.IssuedAt = now.Add(-6 * time.Second)
			case "future":
				v.IssuedAt = now.Add(2 * time.Second)
			case "hash":
				v.ProfileDigest = strings.Repeat("c", 64)
			case "lineage":
				v.Lineage = ""
			}
			b, _ := json.Marshal(v)
			if mode == "duplicate" {
				b = []byte(strings.Replace(string(b), `"version":`, `"version":"provider-path-profiles/v1","version":`, 1))
			}
			if mode == "alias" {
				b = []byte(strings.Replace(string(b), `"sequence":`, `"Sequence":`, 1))
			}
			if _, err := Decode(b, "controller", nonce, now); err == nil {
				t.Fatal("untrusted snapshot accepted")
			}
		})
	}
}
