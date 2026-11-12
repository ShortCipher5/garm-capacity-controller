// These offline static checks do not qualify Docker or installed infrastructure.
package deployment

import (
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
		if strings.HasPrefix(line, "EXPOSE ") || strings.Contains(line, "--mount=type=secret") || strings.Contains(line, "allow-experimental-provisioning") {
			t.Fatal("generic image cannot expose listeners, ingest secrets or enable provisioning")
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
