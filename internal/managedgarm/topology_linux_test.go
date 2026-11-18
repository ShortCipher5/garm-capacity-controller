package managedgarm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func topologyFixture() (Specification, [3]container) {
	s := Specification{GarmServerFingerprint: strings.Repeat("1", 64), DockerSocket: "/run/docker.sock", GarmID: strings.Repeat("a", 64), ControllerID: strings.Repeat("b", 64), IngressID: strings.Repeat("c", 64), GarmImage: "sha256:" + strings.Repeat("d", 64), ControllerImage: "sha256:" + strings.Repeat("e", 64), IngressImage: "sha256:" + strings.Repeat("f", 64), AdminSecretDirectory: "/private/deployment/supervisor-admin"}
	var out [3]container
	for i, id := range []string{s.GarmID, s.ControllerID, s.IngressID} {
		v := &out[i]
		v.Config.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
		v.Config.WorkingDir = "/"
		v.ID = id
		v.Image = []string{s.GarmImage, s.ControllerImage, s.IngressImage}[i]
		v.Config.User = []string{"65530:65530", "65532:65532", "65531:65531"}[i]
		v.State.Running = true
		v.State.Pid = 100 + i
		v.HostConfig.ReadonlyRootfs = true
		v.HostConfig.CapDrop = []string{"ALL"}
		v.HostConfig.SecurityOpt = []string{"no-new-privileges:true"}
		v.HostConfig.NetworkMode = "container:" + s.GarmID
	}
	out[0].HostConfig.NetworkMode = "managed_default"
	out[0].Config.Cmd = []string{"/usr/bin/garm", "-config", "/etc/garm/garm.toml"}
	out[1].Config.Cmd = []string{"/usr/local/bin/capacity-gateway", "-config", "/run/controlplane/config/gateway.json"}
	out[2].Config.Cmd = []string{"/usr/local/bin/capacity-stock-ingress", "/run/controlplane/ingress/ingress.json"}
	for i := range out {
		out[i].Path = out[i].Config.Cmd[0]
		out[i].Args = append([]string(nil), out[i].Config.Cmd[1:]...)
	}
	mounts := []map[string]string{{"/etc/garm": "garm-config", "/var/lib/garm": "garm-state"}, {"/run/controlplane/config": "controller-config", "/var/lib/capacity-gateway": "controller-state"}, {"/run/controlplane/ingress": "ingress"}}
	for i, role := range mounts {
		for target, source := range role {
			out[i].Mounts = append(out[i].Mounts, struct {
				Source, Destination string
				RW                  bool
			}{"/private/deployment/" + source, target, strings.HasSuffix(source, "state")})
		}
	}
	return s, out
}
func TestOwnedTopologyRejectsBypasses(t *testing.T) {
	s, c := topologyFixture()
	if !validSpec(s) || validateContainers(s, c) != nil {
		t.Fatal("fixture")
	}
	for _, mode := range []string{"env", "workdir", "entrypoint", "actual-path", "actual-args", "hostnet", "othernamespace", "root", "privileged", "capability", "writable-root", "secret-to-ingress", "secret-writable", "garm-secret-to-controller", "other-image", "extra-mount", "missing-mount", "command", "stopped", "published-admin", "host-pid"} {
		t.Run(mode, func(t *testing.T) {
			s, c := topologyFixture()
			switch mode {
			case "env":
				c[0].Config.Env = append(c[0].Config.Env, "LD_PRELOAD=/evil.so")
			case "workdir":
				c[0].Config.WorkingDir = "/tmp"
			case "entrypoint":
				c[0].Config.Entrypoint = []string{"/bin/sh"}
			case "actual-path":
				c[0].Path = "/bin/sh"
			case "actual-args":
				c[0].Args = []string{"-c", "evil"}
			case "hostnet":
				c[0].HostConfig.NetworkMode = "host"
			case "othernamespace":
				c[1].HostConfig.NetworkMode = "bridge"
			case "root":
				c[2].Config.User = "0:0"
			case "privileged":
				c[0].HostConfig.Privileged = true
			case "capability":
				c[1].HostConfig.CapAdd = []string{"NET_ADMIN"}
			case "writable-root":
				c[0].HostConfig.ReadonlyRootfs = false
			case "secret-to-ingress":
				c[2].Mounts[0].Source = s.AdminSecretDirectory
			case "secret-writable":
				c[1].Mounts[0].Source = s.AdminSecretDirectory
			case "garm-secret-to-controller":
				c[1].Mounts[0].Source = "/private/deployment/garm-config"
			case "other-image":
				c[0].Image = c[1].Image
			case "extra-mount":
				c[0].Mounts = append(c[0].Mounts, struct {
					Source, Destination string
					RW                  bool
				}{"/var/run/docker.sock", "/var/run/docker.sock", false})
			case "missing-mount":
				c[1].Mounts = nil
			case "command":
				c[1].Config.Cmd = []string{"sh"}
			case "stopped":
				c[0].State.Running = false
			case "published-admin":
				c[0].HostConfig.PortBindings = map[string][]struct{ HostIP, HostPort string }{"9998/tcp": {{"0.0.0.0", "9998"}}}
			case "host-pid":
				c[2].HostConfig.PidMode = "host"
			}
			if validateContainers(s, c) == nil {
				t.Fatal("bypass accepted")
			}
		})
	}
}
func TestMissingActualEvidenceCannotMintObservation(t *testing.T) {
	s, _ := topologyFixture()
	s.DockerSocket = filepath.Join(t.TempDir(), "missing.sock")
	o, e := Observe(context.Background(), s)
	if e == nil || !o.ObservedAt().IsZero() || o.Namespace() != "" {
		t.Fatal("manifest minted observation")
	}
	var copy Observation
	if json.Unmarshal([]byte(`{"namespace":"net:[123]","at":"2026-01-01T00:00:00Z"}`), &copy) != nil {
		t.Fatal("decode")
	}
	if !copy.ObservedAt().IsZero() {
		t.Fatal("JSON minted observation")
	}
}
func TestGeneratedTopologyKeepsAdminPrivateAndNeverStarts(t *testing.T) {
	s, _ := topologyFixture()
	p := Preparation{Directory: "/private/deployment", GarmImage: s.GarmImage, ControllerImage: s.ControllerImage, IngressImage: s.IngressImage}
	raw, e := json.Marshal(composeDocument(p, strings.Repeat("1", 64)))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(raw), "9998:9998") || strings.Contains(string(raw), "docker.sock") || strings.Contains(string(raw), "supervisor-admin") || strings.Contains(string(raw), "host\"") {
		t.Fatal("public management or privileged mount")
	}
	for _, expected := range []string{"8443:8443", "service:garm", "managed-control-plane", "no-new-privileges:true"} {
		if !strings.Contains(string(raw), expected) {
			t.Fatal(expected)
		}
	}
	if os.Geteuid() != 0 {
		p.Directory = filepath.Join(t.TempDir(), "new")
		if Prepare(p) == nil {
			t.Fatal("unprivileged provisioning accepted")
		}
		if _, e := os.Stat(p.Directory); !os.IsNotExist(e) {
			t.Fatal("failed prepare changed filesystem")
		}
	}
}

func TestNamespaceHandleCannotBeMintedOrReused(t *testing.T) {
	for _, o := range []Observation{{}, {garmPID: os.Getpid(), processStart: 1, namespaceIno: 1}} {
		if f, e := o.OpenNetworkNamespace(); e == nil {
			f.Close()
			t.Fatal("unverified namespace accepted")
		}
	}
}
func TestProcessStartParser(t *testing.T) {
	b := "123 (a tricky ) comm) S " + strings.Repeat("0 ", 18) + "12345 0"
	if n, e := parseProcessStart([]byte(b)); e != nil || n != 12345 {
		t.Fatalf("%d %v", n, e)
	}
	for _, v := range []string{"", "123 (x) S", "123 (x) S " + strings.Repeat("0 ", 20)} {
		if _, e := parseProcessStart([]byte(v)); e == nil {
			t.Fatal("bad stat accepted")
		}
	}
	if n, e := processStartTime(os.Getpid()); e != nil || n == 0 {
		t.Fatalf("real stat: %d %v", n, e)
	}
}

func TestPrivilegedAncestorsRejectWritableParent(t *testing.T) {
	// /tmp is deliberately writable even when its child is mode0700.
	d := t.TempDir()
	if err := rootControlledAncestors(d); err == nil {
		t.Fatal("writable ancestor trusted")
	}
	info, err := os.Stat("/")
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	err = rootControlledAncestors("/")
	if (err == nil) != (st.Uid == 0 && info.Mode().Perm()&0022 == 0) {
		t.Fatalf("root ownership was not enforced: %v", err)
	}
}
