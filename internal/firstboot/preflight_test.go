package firstboot

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativepreflight"
	"golang.org/x/crypto/ssh"
	"strings"
	"testing"
	"time"
)

func TestPreflightReviewBindsExactTargetAndArtifact(t *testing.T) {
	r, _ := fixture(t)
	target := DeploymentTarget{Request: r, CredentialAlias: "admin", PreflightSHA256: strings.Repeat("a", 64)}
	base := preflightRecipe(target)
	target.PreflightSHA256 = strings.Repeat("b", 64)
	if base.Digest == preflightRecipe(target).Digest {
		t.Fatal("artifact drift")
	}
	target.Request.ReviewDigest = strings.Repeat("c", 64)
	if base.Digest == preflightRecipe(target).Digest {
		t.Fatal("target drift")
	}
	if strings.Contains(preflightScript, "installer.py") {
		t.Fatal("installer reused")
	}
}
func TestPreflightInvalidReviewNeverResolvesOrWrites(t *testing.T) {
	r, _ := fixture(t)
	store := &memoryStore{}
	calls := 0
	s, e := NewService(map[string]DeploymentTarget{"one": {Request: r, CredentialAlias: "admin", PreflightSHA256: strings.Repeat("a", 64)}}, store, func(context.Context, string) (ssh.Signer, error) { calls++; return nil, ErrPreflight })
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = s.Preflight(context.Background(), "one", "wrong"); e == nil || calls != 0 || store.op != nil {
		t.Fatal("unreviewed operation")
	}
	targets, e := s.Targets(context.Background())
	if e != nil || targets[0].Preflight == nil {
		t.Fatal("recipe absent")
	}
	if _, e = s.Preflight(context.Background(), "one", targets[0].Preflight.Digest); e == nil || calls != 1 || store.op != nil {
		t.Fatal("diagnostic changed install journal")
	}
}
func TestPreflightCannotReturnQualifiedReport(t *testing.T) {
	r := nativepreflight.Report{Version: "native-host-preflight/v1", State: "unqualified_no_activation", ObservedAt: time.Now(), Checks: []nativepreflight.Check{{Name: "arc", State: "unqualified", Required: "hard bound unavailable"}}}
	if validatePreflightReport(r) != nil {
		t.Fatal("valid diagnostic")
	}
	r.State = "ready"
	if validatePreflightReport(r) == nil {
		t.Fatal("ready accepted")
	}
	r.State = "unqualified_no_activation"
	r.Checks[0].State = "qualified"
	if validatePreflightReport(r) == nil {
		t.Fatal("qualified check")
	}
}
