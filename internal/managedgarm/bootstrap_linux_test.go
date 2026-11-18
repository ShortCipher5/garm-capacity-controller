package managedgarm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bootstrapFixture(t *testing.T) *bootstrapFiles {
	t.Helper()
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	files, e := openBootstrapFiles(dir, uint32(os.Geteuid()))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { files.root.Close() })
	c := credential{Username: "capacitycontroller", Password: strings.Repeat("a", 64), Email: "capacity-controller@localhost.invalid", FullName: "Managed capacity controller"}
	if e = files.create("bootstrap-user.json", c); e != nil {
		t.Fatal(e)
	}
	return files
}
func bootstrapResponder(calls *int, lost bool) bootstrapExchange {
	return func(_ context.Context, method, path, token string, body any) ([]byte, error) {
		switch path {
		case "/api/v1/first-run":
			*calls++
			if lost {
				return nil, errors.New("lost ACK")
			}
			return []byte(`{"id":"11111111-1111-4111-8111-111111111111","username":"capacitycontroller","email":"capacity-controller@localhost.invalid","is_admin":true,"enabled":true}`), nil
		case "/api/v1/auth/login":
			return []byte(`{"token":"private-test-token"}`), nil
		case "/api/v1/controller":
			if token != "private-test-token" {
				return nil, ErrBoundary
			}
			return []byte(`{"controller_id":"22222222-2222-4222-8222-222222222222"}`), nil
		}
		return nil, ErrBoundary
	}
}
func TestBootstrapOneShotRecoversLostResponseWithoutFirstRunReplay(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "lost"}[lost], func(t *testing.T) {
			files := bootstrapFixture(t)
			calls := 0
			record, token, e := bootstrapProtocol(context.Background(), files, strings.Repeat("b", 64), bootstrapResponder(&calls, lost))
			if e != nil || calls != 1 || token == "" || record.ControllerID == "" {
				t.Fatal(calls, e)
			}
			again, _, e := bootstrapProtocol(context.Background(), files, strings.Repeat("b", 64), bootstrapResponder(&calls, lost))
			if e != nil || again != record || calls != 1 {
				t.Fatal("replayed first-run", calls, e)
			}
			raw, e := files.root.ReadFile("bootstrap-record.json")
			if e != nil || strings.Contains(string(raw), "private-test-token") || strings.Contains(string(raw), strings.Repeat("a", 64)) {
				t.Fatal("secret in record", e)
			}
		})
	}
}
func TestBootstrapAttemptSurvivesFailedLoginAndRejectsChangedCredential(t *testing.T) {
	files := bootstrapFixture(t)
	calls := 0
	fail := func(_ context.Context, _, path, _ string, _ any) ([]byte, error) {
		if path == "/api/v1/first-run" {
			calls++
		}
		return nil, ErrBoundary
	}
	for i := 0; i < 2; i++ {
		if _, _, e := bootstrapProtocol(context.Background(), files, strings.Repeat("b", 64), fail); e == nil {
			t.Fatal("unexpected success")
		}
	}
	if calls != 1 {
		t.Fatal("replayed unresolved first-run", calls)
	}
	if _, _, e := bootstrapProtocol(context.Background(), files, strings.Repeat("c", 64), bootstrapResponder(&calls, false)); e == nil || calls != 1 {
		t.Fatal("adopted different container")
	}
}
func TestBootstrapReadsRejectSymlinkAndHardlink(t *testing.T) {
	files := bootstrapFixture(t)
	path := filepath.Join(files.root.Name(), "bootstrap-user.json")
	if os.Link(path, filepath.Join(files.root.Name(), "alias")) != nil {
		t.Fatal("fixture link")
	}
	var c credential
	if files.read("bootstrap-user.json", &c) == nil {
		t.Fatal("accepted multiply linked credential")
	}
	if os.Symlink(path, filepath.Join(files.root.Name(), "symlink")) != nil {
		t.Fatal("fixture symlink")
	}
	if files.read("symlink", &c) == nil {
		t.Fatal("accepted symlink")
	}
}
func TestBootstrapAttemptWrittenBeforeNetwork(t *testing.T) {
	files := bootstrapFixture(t)
	calls := 0
	check := func(ctx context.Context, m, p, token string, body any) ([]byte, error) {
		var a bootstrapAttempt
		if files.read("bootstrap-attempt.json", &a) != nil || a.Version != "managed-garm-bootstrap/v1" {
			t.Fatal("network before durable attempt")
		}
		return bootstrapResponder(&calls, false)(ctx, m, p, token, body)
	}
	if _, _, e := bootstrapProtocol(context.Background(), files, strings.Repeat("b", 64), check); e != nil {
		t.Fatal(e)
	}
	var record bootstrapRecord
	files.read("bootstrap-record.json", &record)
	record.ControllerID = "33333333-3333-4333-8333-333333333333"
	raw, _ := json.Marshal(record)
	os.WriteFile(filepath.Join(files.root.Name(), "bootstrap-record.json"), raw, 0600)
	if _, _, e := bootstrapProtocol(context.Background(), files, strings.Repeat("b", 64), check); e == nil {
		t.Fatal("changed stock controller accepted")
	}
}
