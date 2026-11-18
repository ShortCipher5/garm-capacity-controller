package managedgarm

import (
	"context"
	"strings"
	"testing"
)

func TestScopedMetadataRejectsUnsafeRoutesBeforeIO(t *testing.T) {
	s := &Session{}
	pool := "11111111-1111-4111-8111-111111111111"
	for _, v := range []struct{ pool, name, file, token string }{{pool, "../secret", "runner", "token"}, {pool, "runner", "../../admin", "token"}, {pool, "runner", "credentials", "bad\nvalue"}, {"invalid", "runner", "runner", "token"}} {
		if _, e := s.ReadScopedMetadata(context.Background(), v.pool, v.name, v.file, v.token, nil, nil); e == nil {
			t.Fatal("unsafe metadata input accepted")
		}
	}
}
func TestPendingMetadataIdentityAndRunnerCorrelation(t *testing.T) {
	pool := "11111111-1111-4111-8111-111111111111"
	raw := `{"id":"22222222-2222-4222-8222-222222222222","name":"runner","pool_id":"` + pool + `","provider_name":"native","agent_id":123,"provider_id":"","status":"pending_create","runner_status":"pending","scale_set_id":0}`
	v, e := parsePendingMetadata([]byte(raw), pool, "runner", "native")
	if e != nil {
		t.Fatal(e)
	}
	runner := `{"agentId":123,"agentName":"runner","ephemeral":true}`
	if !metadataRunnerMatches([]byte(runner), v) {
		t.Fatal("valid registration rejected")
	}
	for _, bad := range []string{strings.Replace(runner, "123", "124", 1), strings.Replace(runner, "true", "false", 1), strings.Replace(runner, "runner", "other", 1), `{"agentId":123,"AgentId":123,"agentName":"runner","ephemeral":true}`} {
		if metadataRunnerMatches([]byte(bad), v) {
			t.Fatal("ambiguous/foreign runner accepted")
		}
	}
	for _, bad := range []string{strings.Replace(raw, "pending_create", "running", 1), strings.Replace(raw, `"provider_id":""`, `"provider_id":"native-id"`, 1), strings.Replace(raw, `"scale_set_id":0`, `"scale_set_id":1`, 1), strings.Replace(raw, `"name":"runner"`, `"name":"runner","Name":"foreign"`, 1)} {
		if _, e := parsePendingMetadata([]byte(bad), pool, "runner", "native"); e == nil {
			t.Fatal("non-pending/ambiguous source accepted")
		}
	}
}

func TestScopedMetadataKeepsCredentialRolesAndRejectsDrift(t *testing.T) {
	pool := "11111111-1111-4111-8111-111111111111"
	repo := "33333333-3333-4333-8333-333333333333"
	for _, drift := range []bool{false, true} {
		runnerReads := 0
		read := func(path, token string) ([]byte, error) {
			if strings.HasPrefix(path, "/api/v1/metadata/") {
				if token != "INSTANCE" {
					t.Fatal("admin credential sent to metadata")
				}
			} else if token != "ADMIN" {
				t.Fatal("instance credential used as admin")
			}
			switch path {
			case "/api/v1/pools/" + pool:
				return []byte(`{"id":"` + pool + `","repo_id":"` + repo + `","provider_name":"native","flavor":"small","image":"img","os_type":"linux","os_arch":"amd64"}`), nil
			case "/api/v1/instances/runner":
				return []byte(`{"id":"22222222-2222-4222-8222-222222222222","name":"runner","pool_id":"` + pool + `","provider_name":"native","agent_id":123,"status":"pending_create","runner_status":"pending"}`), nil
			case "/api/v1/metadata/credentials/runner":
				runnerReads++
				if drift && runnerReads > 1 {
					return []byte(`{"agentId":124,"agentName":"runner","ephemeral":true}`), nil
				}
				return []byte(`{"agentId":123,"agentName":"runner","ephemeral":true}`), nil
			case "/api/v1/metadata/credentials/credentials":
				return []byte("ORIGINAL SECRET BYTES"), nil
			default:
				t.Fatal("unexpected private request", path)
				return nil, ErrBoundary
			}
		}
		data, e := readScopedMetadata(read, "ADMIN", pool, "runner", "credentials", "INSTANCE", map[string]bool{"native": true}, map[string]bool{repo: true})
		if drift {
			if e == nil || len(data) != 0 {
				t.Fatal("drift leaked credentials")
			}
		} else if e != nil || string(data) != "ORIGINAL SECRET BYTES" {
			t.Fatal("original bytes not preserved", e)
		}
	}
}

func TestBadInstanceReadDoesNotEraseRootSession(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		status       int
		revoked      bool
	}{
		{"GET", "/api/v1/metadata/credentials/runner", 401, false},
		{"GET", "/api/v1/metadata/credentials/credentials", 403, false},
		{"GET", "/api/v1/metadata/credentials/credentials_rsaparams", 404, false},
		{"GET", "/api/v1/instances/runner", 404, false},
		{"GET", "/api/v1/instances", 401, true},
		{"GET", "/api/v1/pools", 401, true},
		{"GET", "/api/v1/metadata/credentials/unknown", 401, true},
		{"PUT", "/api/v1/metadata/credentials/runner", 401, true},
		{"GET", "/api/v1/metadata/credentials/runner", 500, true},
	} {
		s := &Session{token: "ROOT ADMIN"}
		if s.checkResponseStatus(tc.method, tc.path, tc.status) == nil {
			t.Fatal("failure accepted")
		}
		if s.revoked != tc.revoked || (!tc.revoked && s.token != "ROOT ADMIN") {
			t.Fatalf("wrong session effect %+v", tc)
		}
	}
}
