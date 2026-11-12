package runnercontracts

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These local tests check only gate hygiene and pinned source availability.
// They NEVER substitute for the compiled .NET lifecycle test workflow.
func TestQualificationGateDoesNotInstallOrSuppressSecurityChecks(t *testing.T) {
	script, err := os.ReadFile("test.sh")
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := os.ReadFile("../../.github/workflows/runner-lifecycle-contracts.yml")
	if err != nil {
		t.Fatal(err)
	}
	joined := string(script) + string(workflow)
	for _, bad := range []string{"setup-dotnet@", "apt-get install", "dotnet-install.sh", "NuGetAudit=false", "TreatWarningsAsErrors=false", "permissions: write-all", "secrets.", "curl ", "wget "} {
		if strings.Contains(joined, bad) {
			t.Fatalf("unapproved installer/security relaxation in hosted gate: %s", bad)
		}
	}
	if !strings.Contains(joined, "contents: read") || !strings.Contains(joined, "persist-credentials: false") || !strings.Contains(joined, "--filter 'FullyQualifiedName~RetirementSourceContracts'") || !strings.Contains(joined, "actual != expected") || !strings.Contains(joined, "env -i PATH=") {
		t.Fatal("missing least-privilege/source-test execution guard")
	}
	if strings.Contains(string(script), `mkdir -p "$work.results"`) || !strings.Contains(string(workflow), "steps.lifecycle_contracts.outputs.results_created == 'true'") {
		t.Fatal("output reuse or unconditional artifact upload")
	}
	if output, err := exec.Command("bash", "-n", "test.sh").CombinedOutput(); err != nil {
		t.Fatalf("shell syntax: %s %v", output, err)
	}
	overlay, err := os.ReadFile("RetirementSourceContracts.cs")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(overlay), "[Fact]") != 6 || !strings.Contains(string(overlay), "d8b82f44a9ec0258904d5869ae683c31d5aac299") {
		t.Fatal("unexpected compiled contract count/source pin")
	}
}

func TestExactPinnedSourceWhenExplicitlySupplied(t *testing.T) {
	source := os.Getenv("GCC_PINNED_ACTIONS_RUNNER_SOURCE")
	if source == "" {
		t.Skip("Source hash check requires an explicit disposable source path; compiled .NET tests run only in dedicated hosted gate")
	}
	output, err := exec.Command("bash", "test.sh", "--check-source", source).CombinedOutput()
	if err != nil || !strings.Contains(string(output), "Compiled .NET lifecycle tests NOT RUN") {
		t.Fatalf("source-only verification: %s %v", output, err)
	}
}

func TestPreexistingOrSymlinkedOutputsRefusedBeforeAnySideEffect(t *testing.T) {
	for _, suffix := range []string{"", ".results", ".home", ".tmp", ".packages"} {
		for _, kind := range []string{"file", "directory", "symlink", "dangling-symlink"} {
			t.Run(suffix+"/"+kind, func(t *testing.T) {
				parent := t.TempDir()
				work := filepath.Join(parent, "source")
				blocked := work + suffix
				var sentinel string
				switch kind {
				case "file":
					sentinel = blocked
					if err := os.WriteFile(blocked, []byte("untouched"), 0600); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Mkdir(blocked, 0700); err != nil {
						t.Fatal(err)
					}
					sentinel = filepath.Join(blocked, "sentinel")
					if err := os.WriteFile(sentinel, []byte("untouched"), 0600); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					target := filepath.Join(parent, "existing-target")
					if err := os.Mkdir(target, 0700); err != nil {
						t.Fatal(err)
					}
					sentinel = filepath.Join(target, "sentinel")
					if err := os.WriteFile(sentinel, []byte("untouched"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, blocked); err != nil {
						t.Fatal(err)
					}
				case "dangling-symlink":
					if err := os.Symlink(filepath.Join(parent, "missing-target"), blocked); err != nil {
						t.Fatal(err)
					}
				}
				before, err := os.Lstat(blocked)
				if err != nil {
					t.Fatal(err)
				}
				outputFile := filepath.Join(parent, "workflow-output")
				if err := os.WriteFile(outputFile, []byte("before\n"), 0600); err != nil {
					t.Fatal(err)
				}
				command := exec.Command("bash", "test.sh", work)
				command.Env = append(os.Environ(), "GITHUB_OUTPUT="+outputFile)
				output, err := command.CombinedOutput()
				var exited *exec.ExitError
				if !errors.As(err, &exited) || exited.ExitCode() != 2 || !strings.Contains(string(output), "Requires NEW absolute source and isolated output paths.") {
					t.Fatalf("preexisting output not rejected first: %s %v", output, err)
				}
				after, err := os.Lstat(blocked)
				if err != nil || !os.SameFile(before, after) {
					t.Fatal("existing path replaced", err)
				}
				if sentinel != "" {
					value, err := os.ReadFile(sentinel)
					if err != nil || string(value) != "untouched" {
						t.Fatal("existing output changed", err)
					}
				}
				for _, other := range []string{"", ".results", ".home", ".tmp", ".packages"} {
					if other != suffix {
						if _, err := os.Lstat(work + other); !errors.Is(err, os.ErrNotExist) {
							t.Fatal("partial outputs created before preflight refusal", other, err)
						}
					}
				}
				value, err := os.ReadFile(outputFile)
				if err != nil || string(value) != "before\n" {
					t.Fatal("old results acknowledged for artifact upload", err)
				}
			})
		}
	}
}

func TestSecurityOverlayIsOnlyTheExplicitOfficialDependencyUpgrade(t *testing.T) {
	data, err := os.ReadFile("Directory.Build.targets")
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	starts := []string{}
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch node := token.(type) {
		case xml.StartElement:
			starts = append(starts, node.Name.Local)
			if node.Name.Local == "PackageReference" {
				attrs := map[string]string{}
				for _, a := range node.Attr {
					attrs[a.Name.Local] = a.Value
				}
				if len(attrs) != 2 || attrs["Update"] != "System.Security.Cryptography.Pkcs" || attrs["Version"] != "8.0.2" {
					t.Fatal("unexpected dependency override", attrs)
				}
			} else if len(node.Attr) != 0 {
				t.Fatal("unexpected MSBuild attributes")
			}
		case xml.CharData:
			if strings.TrimSpace(string(node)) != "" {
				t.Fatal("unexpected MSBuild content")
			}
		}
	}
	if strings.Join(starts, ",") != "Project,ItemGroup,PackageReference" {
		t.Fatal("security overlay may not alter build/audit policy", starts)
	}
	script, err := os.ReadFile("test.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "assets['libraries']") || !strings.Contains(string(script), "exact_production_dependency_graph': False") || !strings.Contains(string(script), "cmp \"$override\" \"$here/Directory.Build.targets\"") {
		t.Fatal("resolved dependency/provenance verification missing")
	}
}

func TestLocalCoordinatorRemainsDisposableAndCannotBeRetirementAuthority(t *testing.T) {
	code, err := os.ReadFile("LocalIntakeFenceExperiment.cs")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"HttpClient", "Process.Start", "Cancel()", "ShutdownAsync(", "DeleteRunner(", "SqliteConnection", "CancellationTokenSource"} {
		if strings.Contains(string(code), forbidden) {
			t.Fatal("local experiment gained side effects", forbidden)
		}
	}
	contracts, err := os.ReadFile("LocalIntakeFenceContracts.cs")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(contracts), "[Fact]") != 6 || !strings.Contains(string(contracts), "LocalPollSealAlsoSuppressesControlTrafficSoIsNotAQualifiedDrain") {
		t.Fatal("missing explicit unqualified-control counterexample")
	}
	script, err := os.ReadFile("test.sh")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(script), "for overlay in LocalIntakeFenceExperiment.cs LocalIntakeFenceContracts.cs RemoteControlInterfaceContracts.cs") != 2 || !strings.Contains(string(script), "Exactly sixteen compiled lifecycle contract tests") {
		t.Fatal("new tests must be copied, unchanged, and actually execute")
	}
	for _, production := range []string{"../../internal/gateway", "../../internal/retirement"} {
		files, err := os.ReadDir(production)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if file.IsDir() || strings.HasSuffix(file.Name(), "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(production, file.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "LocalIntakeFenceExperiment") {
				t.Fatal("experiment imported into production", production, file.Name())
			}
		}
	}
}

func TestRemoteInterfaceContractsStayMockedAndNoInstall(t *testing.T) {
	contracts, err := os.ReadFile("RemoteControlInterfaceContracts.cs")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(contracts), "[Fact]") != 4 || !strings.Contains(string(contracts), "ClassicSessionExpiryRecreatesUnlessSkipSessionRecoverIsSet") {
		t.Fatal("actual listener control/recovery contract tests missing")
	}
	javascript, err := os.ReadFile("supervisor_contracts.cjs")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"require('node:child_process')", "require('child_process')", "process.kill(", "execSync(", "spawnSync("} {
		if strings.Contains(string(javascript), forbidden) {
			t.Fatal("real external process capability in supervisor fixture", forbidden)
		}
	}
	script, err := os.ReadFile("test.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "timeout 10s env -i PATH=\"$PATH\" node") || !strings.Contains(string(script), "supervisor-contracts.json") {
		t.Fatal("existing-toolchain bounded clean supervisor fixture missing")
	}
}

func TestActualSupervisorSourceWhenExplicitlySupplied(t *testing.T) {
	source := os.Getenv("GCC_PINNED_ACTIONS_RUNNER_SOURCE")
	if source == "" {
		t.Skip("explicit source required; no automatic download or install")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("existing Node unavailable; no installation attempted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "supervisor_contracts.cjs", source)
	command.Env = []string{"PATH=" + os.Getenv("PATH")}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("actual supervisor-source fixture: %s %v", output, err)
	}
	var result struct {
		Passed       []string `json:"passed"`
		RealChildren int      `json:"real_children_spawned"`
		RealSignals  int      `json:"real_signals_sent"`
		Qualified    bool     `json:"restart_fence_qualified"`
		Authority    bool     `json:"retirement_authority"`
	}
	if err := json.Unmarshal(output, &result); err != nil || len(result.Passed) != 8 || result.RealChildren != 0 || result.RealSignals != 0 || result.Qualified || result.Authority {
		t.Fatal("incorrect supervisor experiment result", string(output), err)
	}
}
