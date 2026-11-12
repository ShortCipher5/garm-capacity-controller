//go:build linux && cgo

package ingressfrontier

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This is a SOURCE-IDENTITY check, not compilation or execution of Incus.
// Native fixtures deliberately represent the source-documented uncertainty.
func TestPinnedIncusSourceIdentity(t *testing.T) {
	root := os.Getenv("GCC_INCUS_FRONTIER_SOURCE")
	if root == "" {
		t.Skip("optional exact Incus source gate not supplied; no native-source qualification claimed")
	}
	expected := map[string]string{
		"cmd/incusd/instances_post.go":             "6bf045a77a8270923af4d3c4ec29e4641e947dcec5b9c5cc1fe6ab30766cb1b2",
		"cmd/incusd/instance_delete.go":            "531bcd6eef56a59afab046a110f5e426695cb07fd0cf61d9f69910c929e198db",
		"internal/server/operations/operations.go": "6945cba46c7d4d92125fbb0623ec962ddc188b04a3ac2fbd5b07f56c7d351bc7",
	}
	contents := map[string]string{}
	for p, want := range expected {
		b, e := os.ReadFile(filepath.Join(root, p))
		if e != nil {
			t.Fatal(e)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(b)); got != want {
			t.Fatalf("source %s changed: %s", p, got)
		}
		contents[p] = string(b)
	}
	post := contents["cmd/incusd/instances_post.go"]
	handler := strings.Index(post, "func instancesPost(")
	if handler < 0 {
		t.Fatal("handler")
	}
	post = post[handler:]
	decode := strings.Index(post, "json.NewDecoder(r.Body).Decode(&req)")
	transaction := strings.Index(post, "s.DB.Cluster.Transaction(")
	if decode < 0 || transaction < decode {
		t.Fatal("source premise changed: decode precedes DB work")
	}
	del := contents["cmd/incusd/instance_delete.go"]
	if !strings.Contains(del, "instance.LoadByProjectAndName(s, projectName, name)") || !strings.Contains(del, "return inst.Delete(false)") {
		t.Fatal("name-targeted native delete premise changed")
	}
	ops := contents["internal/server/operations/operations.go"]
	if !strings.Contains(ops, "s.ShutdownCtx.Err() == context.Canceled") || !strings.Contains(ops, "time.Second * 5") {
		t.Fatal("operation lifecycle premise changed")
	}
}
