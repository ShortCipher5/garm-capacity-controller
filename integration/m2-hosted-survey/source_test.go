package m2hostedsurvey

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestReadOnlyWorkflowCannotRunKernelOrRuntimeOperations(t *testing.T) {
	script, err := os.ReadFile("survey.py")
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := os.ReadFile("../../.github/workflows/m2-hosted-capability-survey.yml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(script) + string(workflow)
	for _, forbidden := range []string{"sudo", "--privileged", "docker.sock", "podman.sock", "--cap-add", "--device", "pull_request_target", "secrets.", "apt-get", "sysctl -", "shell=True"} {
		if strings.Contains(text, forbidden) {
			t.Fatal("not read-only survey", forbidden)
		}
	}
	for _, required := range []string{"contents: read", "persist-credentials: false", "runs-on: ubuntu-24.04", "steps.survey.outputs.results_created == 'true'", "survey.json"} {
		if !strings.Contains(string(workflow), required) {
			t.Fatal("workflow guard missing", required)
		}
	}
	if !strings.Contains(string(script), `"isolation_qualified": False`) || !strings.Contains(string(script), `"execution_authority": "none"`) {
		t.Fatal("survey became authority")
	}
}
func TestPythonSurveyFixturesWithoutActualHostedProbes(t *testing.T) {
	command := exec.Command("python3", "-m", "unittest", "discover", "-s", ".", "-p", "*_test.py")
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "PYTHONDONTWRITEBYTECODE=1"}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("survey unit fixtures: %s %v", output, err)
	}
}
