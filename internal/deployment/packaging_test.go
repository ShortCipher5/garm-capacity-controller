// These offline static checks do not qualify Docker or installed infrastructure.
package deployment

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func repositoryFile(t *testing.T, name string) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate packaging source")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "../..", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestOverlayRemainsOptInReadOnlyAndCredentialIsolated(t *testing.T) {
	var overlay struct {
		Services map[string]struct {
			Image           string   `yaml:"image"`
			Profiles        []string `yaml:"profiles"`
			User            string   `yaml:"user"`
			NetworkMode     string   `yaml:"network_mode"`
			ReadOnly        bool     `yaml:"read_only"`
			CapDrop         []string `yaml:"cap_drop"`
			SecurityOpt     []string `yaml:"security_opt"`
			PidsLimit       int      `yaml:"pids_limit"`
			CPUs            float64  `yaml:"cpus"`
			MemLimit        string   `yaml:"mem_limit"`
			Restart         string   `yaml:"restart"`
			StopGracePeriod string   `yaml:"stop_grace_period"`
			Command         []string `yaml:"command"`
			Volumes         []struct {
				Type     string `yaml:"type"`
				Source   string `yaml:"source"`
				Target   string `yaml:"target"`
				ReadOnly bool   `yaml:"read_only"`
				Bind     struct {
					CreateHostPath *bool `yaml:"create_host_path"`
				} `yaml:"bind"`
			} `yaml:"volumes"`
			Logging struct {
				Driver  string            `yaml:"driver"`
				Options map[string]string `yaml:"options"`
			} `yaml:"logging"`
		} `yaml:"services"`
	}
	decoder := yaml.NewDecoder(strings.NewReader(repositoryFile(t, "deploy/compose.gateway.yaml")))
	decoder.KnownFields(true) // Ports, new privileges or environment need explicit review.
	if err := decoder.Decode(&overlay); err != nil {
		t.Fatal(err)
	}
	gateway, ok := overlay.Services["capacity-gateway"]
	if !ok || len(overlay.Services) != 1 {
		t.Fatal("overlay must not alter existing Garm")
	}
	if gateway.Image != "${CAPACITY_GATEWAY_IMAGE:?set a reviewed immutable gateway image digest}" || strings.Join(gateway.Profiles, ",") != "capacity-preview" || gateway.Restart != "no" {
		t.Fatal("preview must require explicit image/profile, with no automatic restart")
	}
	if gateway.NetworkMode != "service:garm" || gateway.User != "65532:65532" || !gateway.ReadOnly || strings.Join(gateway.CapDrop, ",") != "ALL" || strings.Join(gateway.SecurityOpt, ",") != "no-new-privileges:true" || gateway.PidsLimit != 128 || gateway.StopGracePeriod != "45s" || gateway.CPUs != 1 || gateway.MemLimit != "512m" {
		t.Fatal("loopback namespace and non-root immutable restrictions must remain explicit")
	}
	if strings.Join(gateway.Command, " ") != "-config /run/capacity-private/gateway.local.json" {
		t.Fatal("packaging cannot enable experimental provisioning or secret arguments")
	}
	if len(gateway.Volumes) != 2 {
		t.Fatal("only separate private and durable state binds are allowed")
	}
	for i, v := range gateway.Volumes {
		if v.Type != "bind" || v.Bind.CreateHostPath == nil || *v.Bind.CreateHostPath {
			t.Fatal("private directories must be precreated")
		}
		if i == 0 && (v.Target != "/run/capacity-private" || !v.ReadOnly || v.Source != "${CAPACITY_PRIVATE_DIR:?set a precreated private gateway directory}") {
			t.Fatal("credentials must be private and read-only")
		}
		if i == 1 && (v.Target != "/var/lib/capacity-gateway" || v.ReadOnly || v.Source != "${CAPACITY_STATE_DIR:?set a precreated private local ledger directory}") {
			t.Fatal("SQLite needs a durable directory including WAL/SHM")
		}
	}
	if gateway.Logging.Driver != "json-file" || gateway.Logging.Options["max-size"] != "10m" || gateway.Logging.Options["max-file"] != "3" {
		t.Fatal("preview logs must be bounded")
	}
}

func TestContainerBuildContract(t *testing.T) {
	dockerfile := repositoryFile(t, "Dockerfile")
	for _, required := range []string{
		"RUN CGO_ENABLED=1 go build -trimpath -buildvcs=false -o /out/capacity-gateway ./cmd/capacity-gateway",
		"RUN CGO_ENABLED=1 go build -trimpath -buildvcs=false -o /out/capacity-provider ./cmd/capacity-provider",
		"RUN CGO_ENABLED=1 go build -trimpath -buildvcs=false -o /out/capacity-ledger ./cmd/capacity-ledger",
		"FROM runtime-base AS ledger-tool", `ENTRYPOINT ["/usr/local/bin/capacity-ledger"]`, `CMD ["--help"]`,
		"RUN go mod download && go mod verify", "RUN npm ci --ignore-scripts", "RUN npm test && npm run build",
		"FROM scratch AS provider-artifact", "USER 65532:65532", `CMD ["-config", "/run/capacity-private/gateway.local.json"]`,
	} {
		if !strings.Contains(dockerfile, required) {
			t.Fatalf("missing build contract: %s", required)
		}
	}
	for _, line := range strings.Split(dockerfile, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "COPY ") && (strings.Contains(line, " . ") || strings.Contains(line, "private") || strings.Contains(line, "deploy/")) {
			t.Fatal("unrestricted/deployment copies may leak private inputs into layers")
		}
		if strings.HasPrefix(line, "EXPOSE ") || strings.Contains(line, "--mount=type=secret") || strings.Contains(line, "allow-experimental-provisioning") || strings.Contains(line, "allow-experimental-management") {
			t.Fatal("generic image cannot expose listeners, ingest secrets or enable provisioning/management")
		}
	}
	ignore := repositoryFile(t, ".dockerignore")
	first := ""
	for _, line := range strings.Split(ignore, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			first = line
			break
		}
	}
	if first != "**" {
		t.Fatal("Docker context must be denied before source allowlists")
	}
	for _, denied := range []string{"**/*.local.json", "**/*.token", "**/*.pem", "**/*.key", "**/*.db", "**/*.db-*", "**/.env*"} {
		if !strings.Contains(ignore, "\n"+denied+"\n") {
			t.Fatalf("missing sensitive context exclusion: %s", denied)
		}
	}
}

func TestPreviewDocumentsCurrentOptInAndRecoveryBoundaries(t *testing.T) {
	readme := repositoryFile(t, "deploy/README.md")
	for _, required := range []string{"provisioning and ledger management OFF", "admin.management_token_file", "capacity-ledger", "not in the gateway image"} {
		if !strings.Contains(readme, required) {
			t.Fatalf("missing current preview boundary: %s", required)
		}
	}
}

func TestRecoveryToolIsSeparateAndQualificationIsNotDeployment(t *testing.T) {
	dockerfile := repositoryFile(t, "Dockerfile")
	_, ledger, ok := strings.Cut(dockerfile, "FROM runtime-base AS ledger-tool\n")
	if !ok {
		t.Fatal("missing separate recovery image")
	}
	ledger, gateway, ok := strings.Cut(ledger, "FROM runtime-base AS gateway\n")
	if !ok || strings.Contains(gateway, "capacity-ledger") || strings.Contains(ledger, "capacity-gateway") || strings.Contains(ledger, "capacity-admin") {
		t.Fatal("gateway and local recovery target must stay separate")
	}
	plan := repositoryFile(t, "deploy/qualification.md")
	for _, required := range []string{"plan, not executed container qualification", "--network none --read-only --cap-drop ALL", "--user 65532:65532", "No path/secret", "whole source directory", "write-blocked", "no live deployment command"} {
		if !strings.Contains(plan, required) {
			t.Fatalf("missing disposable qualification boundary: %s", required)
		}
	}
}

func TestHostedQualificationHasNoLiveOrSecretInputs(t *testing.T) {
	workflow := repositoryFile(t, ".github/workflows/packaging.yml")
	for _, required := range []string{"runs-on: ubuntu-24.04", "contents: read", "persist-credentials: false", "deploy/runtime/test.sh", "qualification-results/*.json"} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("missing hosted isolation contract: %s", required)
		}
	}
	for _, forbidden := range []string{"pull_request_target", "self-hosted", "secrets.", "write-all", "contents: write"} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("unsafe workflow input: %s", forbidden)
		}
	}
	harness := repositoryFile(t, "deploy/runtime/test.sh")
	for _, required := range []string{"RUNNER_TEMP", "--network none", "--cap-drop ALL", "--security-opt no-new-privileges", "--user 65532:65532", "installed_stack_qualified", "--force-recreate"} {
		if !strings.Contains(harness, required) {
			t.Fatalf("missing disposable runtime boundary: %s", required)
		}
	}
	for _, forbidden := range []string{"--privileged", "/var/run/docker.sock", "docker system prune", "docker login", "compose down -v"} {
		if strings.Contains(harness, forbidden) {
			t.Fatalf("unsafe harness operation: %s", forbidden)
		}
	}
}

func TestUpstreamGarmLeastPrivilegeLabCannotImplyInstalledQualification(t *testing.T) {
	harness := repositoryFile(t, "deploy/runtime/garm-least-privilege.sh")
	for _, required := range []string{"RUNNER_ENVIRONMENT", "github-hosted", "--network none", "--user 65531:65531", "--cap-drop ALL", "no-new-privileges", "umask 077", "CapBnd", "NoNewPrivs", "installed_stack_qualified", "deployment_authorized"} {
		if !strings.Contains(harness, required) {
			t.Fatalf("missing least-privilege lab boundary: %s", required)
		}
	}
	for _, forbidden := range []string{"--privileged", "docker login", "docker system prune", "--network host", "--pid host", "secrets."} {
		if strings.Contains(harness, forbidden) {
			t.Fatalf("unsafe least-privilege lab operation: %s", forbidden)
		}
	}
	plan := repositoryFile(t, "deploy/garm-least-privilege.md")
	for _, required := range []string{"new generated empty state", "not authorization", "missing database", "effective runtime UID", "wrong-mount protection", "job-safe retirement/drain"} {
		if !strings.Contains(plan, required) {
			t.Fatalf("missing installed-stack gate: %s", required)
		}
	}
}

func TestPatchedGarmTransferIsGeneratedStoppedStateOnly(t *testing.T) {
	script := repositoryFile(t, "deploy/runtime/garm-migration/test.sh")
	for _, required := range []string{"RUNNER_ENVIRONMENT:-} = github-hosted", "87e0d61751d4b9ffa15322cff09557f0a1094fdd68464fe840a12ffe01bb0660", "--network none", "--cap-drop ALL", "--security-opt no-new-privileges", "--read-only", "--user 65531:65531", "helper \"$legacy\" 0 seed", "state.py transfer", "missing-main missing-blob mode owner symlink hardlink wrong-controller changed-config interrupted-copy wrong-passphrase wrong-jwt wrong-ca wrong-tls-key", "'installed_stack_qualified':False", "'provider_integration_qualified':False", "'availability_qualified':False"} {
		if !strings.Contains(script, required) {
			t.Fatalf("missing synthetic migration contract: %s", required)
		}
	}
	for _, denied := range []string{"docker push", "docker login", "docker logs", "--privileged", "--network host", "compose up", "chmod 777", "/var/run/docker.sock"} {
		if strings.Contains(script, denied) {
			t.Fatalf("disposable migration must not add live/secret/privilege action: %s", denied)
		}
	}
	workflow := repositoryFile(t, ".github/workflows/packaging.yml")
	for _, required := range []string{"patched-garm-state-transfer-lab:", "run: deploy/runtime/garm-migration/test.sh", "path: qualification-results/garm-state-transfer.json"} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("missing hosted-only transfer gate: %s", required)
		}
	}
	source := repositoryFile(t, "deploy/runtime/garm-migration/store.go.txt")
	for _, required := range []string{"mode=ro&_query_only=1", "blob-controller.db", "st.Nlink == 1", "action == \"seed\"", "require(action == \"guard\")", "commonutil.Unseal", "jwt.WithValidMethods"} {
		if !strings.Contains(source, required) {
			t.Fatalf("missing fail-closed synthetic guard: %s", required)
		}
	}
}
