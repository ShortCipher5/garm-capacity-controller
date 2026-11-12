// These are read-only, hash-pinned source-contract checks, not runtime/drain tests.
package operatinglimits

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudbase/garm-provider-common/cloudconfig"
)

const pinnedGarm = "154638445c3949c1958b01812f69d9a1e4d82684"

func functions(t *testing.T, source []byte) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "source.go", source, 0) // No comments as assertions.
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	for _, d := range f.Decls {
		f, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		var b bytes.Buffer
		if err := format.Node(&b, fset, f); err != nil {
			t.Fatal(err)
		}
		result[f.Name.Name] = b.String()
	}
	return result
}

func require(t *testing.T, body string, phrases ...string) {
	t.Helper()
	for _, phrase := range phrases {
		if !strings.Contains(body, phrase) {
			t.Fatalf("source-contract changed: missing %q", phrase)
		}
	}
}

func TestDefaultNonJITBootstrapIsNotPersistent(t *testing.T) {
	// Imported, compiled dependency is pinned by go.mod/go.sum; inspect templates,
	// never run the shell or contact a registration endpoint.
	for name, source := range map[string]string{"linux": cloudconfig.CloudConfigTemplate, "windows": cloudconfig.WindowsSetupScriptTemplate} {
		t.Run(name, func(t *testing.T) {
			count := 0
			for _, line := range strings.Split(source, "\n") {
				if strings.Contains(line, "./config.") && strings.Contains(line, "--unattended") {
					count++
					if !strings.Contains(line, "--ephemeral") {
						t.Fatal("default registration is no longer explicitly ephemeral; re-review MVP assumptions")
					}
				}
			}
			if count != 2 {
				t.Fatalf("unexpected registration branches: %d", count)
			}
		})
	}
}

func TestPinnedGarmLifecycleOperatingLimits(t *testing.T) {
	root := os.Getenv("GCC_PINNED_GARM_WORKTREE")
	if root == "" {
		t.Skip("requires disposable pinned Garm checkout; ordinary unit pass is NOT lifecycle qualification")
	}
	files := map[string]string{
		"runner/pool/pool.go":          "0ff95bdd8b0c327da9d82694247bd4a81098b10e8a217d1b3089437246ec8e26",
		"workers/scaleset/scaleset.go": "0e67eca7a8539af75ed16cf1b656bfae506f99de8cd4ce6a84f964a2fa46d0d8",
		"runner/scalesets.go":          "2d8ebc0ad8f330cce61c5b488b5df68637b075e58d9800850f66456958f8223a",
	}
	raw := map[string]map[string]string{}
	patched := map[string]map[string]string{}
	for path, digest := range files {
		// Read exact official git objects, even when the checkout has our admission
		// patch. No clone, shell, network, Garm executable or credentials are used.
		out, err := exec.Command("git", "-C", root, "show", pinnedGarm+":"+path).Output()
		if err != nil {
			t.Fatalf("read pinned source %s: %v", path, err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(out)) != digest {
			t.Fatalf("pinned source digest mismatch: %s", path)
		}
		raw[path] = functions(t, out)
		current, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		patched[path] = functions(t, current)
	}
	check := func(t *testing.T, path, function string, phrases ...string) {
		t.Helper()
		body := raw[path][function]
		if body == "" || body != patched[path][function] {
			t.Fatalf("reviewed lifecycle function changed in patched checkout: %s", function)
		}
		require(t, body, phrases...)
	}
	pool := "runner/pool/pool.go"
	scale := "workers/scaleset/scaleset.go"
	t.Run("completion_is_delete_intent_not_native_completion", func(t *testing.T) {
		check(t, pool, "handleCompletedJob", "params.RunnerTerminated", "commonParams.InstancePendingDelete", "r.setInstanceStatus(")
	})
	t.Run("disabled_pool_only_gates_some_paths", func(t *testing.T) {
		check(t, pool, "scaleDownOnePool", "if !pool.Enabled", "params.RunnerIdle", "r.DeleteRunner(instanceToDelete, false, false)")
		check(t, pool, "Start", "r.cleanupRunnersInterruptedInFlight()", `"consolidate[delete_pending]", true)`)
		check(t, pool, "deletePendingInstances", "r.deleteInstanceFromProvider(r.ctx, instance)", "currentStatus != commonParams.InstancePendingForceDelete", "r.store.DeleteInstance(")
	})
	t.Run("timeout_and_absence_are_cleanup_heuristics", func(t *testing.T) {
		check(t, pool, "reapTimedOutRunners", "time.Since(instance.UpdatedAt)", `runner.Status == "offline"`, "r.DeleteRunner(instance, false, false)")
		check(t, pool, "cleanupOrphanedProviderRunners", "runnerNames[instance.Name]", "commonParams.InstancePendingDelete, nil, true")
	})
	t.Run("restart_can_deregister_and_force_mark", func(t *testing.T) {
		check(t, pool, "cleanupRunnersInterruptedInFlight", "commonParams.InstanceCreating, commonParams.InstanceDeleting", "r.DeleteRunner(instance, false, true)")
		check(t, pool, "DeleteRunner", "r.ghcli.RemoveEntityRunner(", "if bypassGHUnauthorizedError", "commonParams.InstancePendingForceDelete", "r.setInstanceStatus(")
	})
	t.Run("manager_stop_has_no_runner_drain_handshake", func(t *testing.T) {
		check(t, pool, "Stop", "close(r.quit)", "return nil")
	})
	t.Run("scaleset_disable_is_not_an_instance_seal", func(t *testing.T) {
		check(t, scale, "handleScaleSetEvent", "!scaleSet.Enabled", "w.listener.Stop()")
		check(t, scale, "handleAutoScale", "w.handleInstanceCleanup(instance)", "w.handleScaleDown()")
		check(t, scale, "handleScaleDown", "params.RunnerTerminated, params.RunnerActive", "scaleSetCli.RemoveRunner(", "commonParams.InstancePendingDelete")
		check(t, scale, "consolidateRunnerState", "w.provider.DeleteInstance(", "commonParams.InstancePendingDelete")
	})
	t.Run("scalesets_explicitly_ephemeral", func(t *testing.T) {
		// Creation is unchanged; Worker.createScaleSet may be updated independently.
		check(t, "runner/scalesets.go", "CreateEntityScaleSet", "Ephemeral:     true")
	})
}
