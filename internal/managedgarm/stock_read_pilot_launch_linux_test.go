//go:build linux && stock_read_pilot_harness

package managedgarm

import (
	"reflect"
	"strings"
	"testing"
)

// The existing joined inspect order is stock/ingress/gateway. Never copy it
// positionally into the observer's stock/gateway/ingress arrays.
type stockPilotFixtureIdentity struct{ Role, ContainerID, Image, ExecutableSHA256 string }

func stockPilotFixtureProfileFromJoined(joined [3]stockPilotFixtureIdentity) (stockReadProfile, error) {
	p := stockReadProfile{Version: "stock-readonly-profile/v1", Reference: stockReadReference, Platform: "linux/amd64", Directory: stockPilotHarnessDirectory}
	for i, role := range []string{"stock", "ingress", "gateway"} {
		if joined[i].Role != role {
			return stockReadProfile{}, ErrBoundary
		}
		destination := []int{0, 2, 1}[i]
		p.IDs[destination] = joined[i].ContainerID
		p.Images[destination] = joined[i].Image
		p.ExecutableSHA256[destination] = joined[i].ExecutableSHA256
	}
	if !validStockReadProfile(p) {
		return stockReadProfile{}, ErrBoundary
	}
	return p, nil
}

// Test-only launch descriptions, not Docker requests or runtime observations.
// A future guarded wrapper must require fresh owned paths/names, independently
// inspect actual results, and never treat a descriptor as verified execution.
type stockPilotLaunchMount struct {
	Source, Destination string
	ReadOnly            bool
}
type stockPilotLaunchDescriptor struct {
	Role, Image, User, WorkingDirectory, Network               string
	Entrypoint, Command, Environment, CapDrop, SecurityOptions []string
	ReadOnlyRoot                                               bool
	PIDMode, IPCMode, RestartPolicy                            string
	PIDs, MemoryBytes, NanoCPUs                                int64
	Mounts                                                     []stockPilotLaunchMount
	Tmpfs                                                      map[string]string
}

func stockPilotLaunchBase(role, image, user, network string) stockPilotLaunchDescriptor {
	return stockPilotLaunchDescriptor{
		Role: role, Image: image, User: user, WorkingDirectory: "/", Network: network,
		Environment: []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		CapDrop:     []string{"ALL"}, SecurityOptions: []string{"no-new-privileges:true"}, ReadOnlyRoot: true,
		PIDMode: "", IPCMode: "private", RestartPolicy: "no", PIDs: 256, MemoryBytes: 512 << 20, NanoCPUs: 1_000_000_000,
	}
}
func stockPilotFixtureStockLaunch() stockPilotLaunchDescriptor {
	d := stockPilotLaunchBase("stock", stockReadImage, "65530:65530", "none")
	d.Entrypoint = []string{"/bin/sh"}
	d.Command = []string{"-ec", `umask 077; exec "$1" -config /run/garm-private/config.toml`, "probe", "/bin/garm"}
	d.Mounts = []stockPilotLaunchMount{
		{stockPilotHarnessDirectory + "/fixture/private", "/run/garm-private", true},
		{stockPilotHarnessDirectory + "/fixture/state", "/var/lib/garm", false},
	}
	d.Tmpfs = map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=16m,uid=65530,gid=65530,mode=0700"}
	return d
}
func stockPilotFixturePeerLaunches(actualStockID, gatewayImage, ingressImage string) ([2]stockPilotLaunchDescriptor, error) {
	var out [2]stockPilotLaunchDescriptor
	if !digest.MatchString(actualStockID) || !strings.HasPrefix(gatewayImage, "sha256:") || !digest.MatchString(strings.TrimPrefix(gatewayImage, "sha256:")) || !strings.HasPrefix(ingressImage, "sha256:") || !digest.MatchString(strings.TrimPrefix(ingressImage, "sha256:")) || gatewayImage == ingressImage || gatewayImage == stockReadImage || ingressImage == stockReadImage {
		return out, ErrBoundary
	}
	network := "container:" + actualStockID
	gateway := stockPilotLaunchBase("gateway", gatewayImage, "65532:65532", network)
	gateway.Entrypoint = []string{"/usr/local/bin/capacity-gateway"}
	gateway.Command = []string{"-config", "/run/capacity-private/gateway.local.json"}
	gateway.Mounts = []stockPilotLaunchMount{
		{stockPilotHarnessDirectory + "/joined/gateway-private", "/run/capacity-private", true},
		{stockPilotHarnessDirectory + "/joined/state", "/var/lib/capacity-gateway", false},
	}
	gateway.Tmpfs = map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=16m,uid=65532,gid=65532,mode=0700"}
	ingress := stockPilotLaunchBase("ingress", ingressImage, "65531:65531", network)
	ingress.Command = []string{"/usr/local/bin/capacity-stock-ingress", "/run/joined-ingress/ingress.json"}
	ingress.Mounts = []stockPilotLaunchMount{{stockPilotHarnessDirectory + "/joined/ingress-private", "/run/joined-ingress", true}}
	return [2]stockPilotLaunchDescriptor{gateway, ingress}, nil
}

func TestStockPilotLaunchDescriptionsMatchObserverContract(t *testing.T) {
	p, containers := stockReadFixture()
	p.Directory = stockPilotHarnessDirectory
	peers, err := stockPilotFixturePeerLaunches(p.IDs[0], p.Images[1], p.Images[2])
	if err != nil {
		t.Fatal(err)
	}
	roles := [3]stockPilotLaunchDescriptor{stockPilotFixtureStockLaunch(), peers[0], peers[1]}
	expectedMounts := stockReadMounts(p)
	for i, d := range roles {
		if d.Image != p.Images[i] || d.User != []string{"65530:65530", "65532:65532", "65531:65531"}[i] || d.WorkingDirectory != "/" || !d.ReadOnlyRoot || d.PIDMode != "" || d.IPCMode != "private" || d.RestartPolicy != "no" || d.PIDs != 256 || d.MemoryBytes != 512<<20 || d.NanoCPUs != 1_000_000_000 || !reflect.DeepEqual(d.CapDrop, []string{"ALL"}) || !reflect.DeepEqual(d.SecurityOptions, []string{"no-new-privileges:true"}) {
			t.Fatal("launch isolation drift")
		}
		c := &containers[i]
		c.Config.User = d.User
		c.Config.WorkingDir = d.WorkingDirectory
		c.Config.Env = d.Environment
		c.Config.Entrypoint = d.Entrypoint
		c.Config.Cmd = d.Command
		c.HostConfig.NetworkMode = d.Network
		c.HostConfig.PidMode = d.PIDMode
		c.HostConfig.IpcMode = d.IPCMode
		c.HostConfig.ReadonlyRootfs = d.ReadOnlyRoot
		c.HostConfig.CapDrop = d.CapDrop
		c.HostConfig.SecurityOpt = d.SecurityOptions
		if len(d.Entrypoint) > 0 {
			c.Path = d.Entrypoint[0]
			c.Args = d.Command
		} else {
			c.Path = d.Command[0]
			c.Args = d.Command[1:]
		}
		c.Mounts = nil
		if len(d.Mounts) != len(expectedMounts[i]) {
			t.Fatal("mount set drift")
		}
		for _, m := range d.Mounts {
			if expectedMounts[i][m.Destination] != m.Source || !strings.HasPrefix(m.Source, stockPilotHarnessDirectory+"/") {
				t.Fatal("foreign mount")
			}
			c.Mounts = append(c.Mounts, struct {
				Source, Destination string
				RW                  bool
			}{m.Source, m.Destination, !m.ReadOnly})
		}
		if i == 2 && len(d.Tmpfs) != 0 || i != 2 && len(d.Tmpfs) != 1 {
			t.Fatal("unexpected tmpfs")
		}
	}
	if validateStockReadContainers(p, containers) != nil {
		t.Fatal("descriptors conflict with actual pure observation grammar")
	}
	// This is a synthetic grammar check, not successful process observation.
	roles[0].Command[0] = "changed"
	if stockPilotFixtureStockLaunch().Command[0] != "-ec" {
		t.Fatal("shared mutable launcher")
	}
}
func TestStockPilotLaunchDescriptionsRefuseUnpinnedJoins(t *testing.T) {
	p, _ := stockReadFixture()
	for _, args := range [][3]string{
		{"", p.Images[1], p.Images[2]},
		{"container:" + p.IDs[0], p.Images[1], p.Images[2]},
		{p.IDs[0], "latest", p.Images[2]},
		{p.IDs[0], p.Images[1], p.Images[1]},
		{p.IDs[0], stockReadImage, p.Images[2]},
		{p.IDs[0], p.Images[1], p.Images[2] + "\n"},
	} {
		got, err := stockPilotFixturePeerLaunches(args[0], args[1], args[2])
		if err == nil || got[0].Role != "" || got[1].Role != "" {
			t.Fatal("unbound peer launch")
		}
	}
}

func TestStockPilotJoinedRoleOrderIsMappedExplicitly(t *testing.T) {
	p, _ := stockReadFixture()
	p.Directory = stockPilotHarnessDirectory
	joined := [3]stockPilotFixtureIdentity{
		{"stock", p.IDs[0], p.Images[0], p.ExecutableSHA256[0]},
		{"ingress", p.IDs[2], p.Images[2], p.ExecutableSHA256[2]},
		{"gateway", p.IDs[1], p.Images[1], p.ExecutableSHA256[1]},
	}
	got, err := stockPilotFixtureProfileFromJoined(joined)
	if err != nil || got != p {
		t.Fatal("joined role mapping changed identity")
	}
	swapped := joined
	swapped[1], swapped[2] = swapped[2], swapped[1]
	if _, err := stockPilotFixtureProfileFromJoined(swapped); err == nil {
		t.Fatal("unlabelled positional role swap accepted")
	}
	duplicate := joined
	duplicate[1].ContainerID = duplicate[0].ContainerID
	if _, err := stockPilotFixtureProfileFromJoined(duplicate); err == nil {
		t.Fatal("duplicate container accepted")
	}
}
