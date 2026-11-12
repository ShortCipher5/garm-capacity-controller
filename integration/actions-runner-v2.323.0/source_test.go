package runnercontracts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
