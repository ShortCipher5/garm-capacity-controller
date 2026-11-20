//go:build linux && stock_read_pilot_harness

package managedgarm

import (
	"reflect"
	"strings"
	"testing"
)

// Keep the immutable pins obtained from a separately reviewed build/run
// distinct from current full IDs returned by the launch of owned containers.
// Neither input may come from the observation being compared. This function
// checks equality, not provenance, custody, deployed behavior or authority.
type stockPilotJoinedAssetPin struct{ Role, Image, ExecutableSHA256 string }
type stockPilotJoinedLaunchID struct{ Role, ContainerID string }
type stockPilotFixtureIdentity struct{ Role, ContainerID, Image, ExecutableSHA256 string }

// Launch/evidence/inspect order is stock/ingress/gateway. Never copy it
// positionally into the observer's stock/gateway/ingress arrays.
func stockPilotFixtureProfileFromJoined(
	pins [3]stockPilotJoinedAssetPin,
	launched [3]stockPilotJoinedLaunchID,
	observed [3]stockPilotFixtureIdentity,
) (stockReadProfile, error) {
	p := stockReadProfile{Version: "stock-readonly-profile/v1", Reference: stockReadReference, Platform: "linux/amd64", Directory: stockPilotHarnessDirectory}
	for i, role := range []string{"stock", "ingress", "gateway"} {
		pin, launch, actual := pins[i], launched[i], observed[i]
		if pin.Role != role || launch.Role != role || actual.Role != role ||
			actual.ContainerID != launch.ContainerID || actual.Image != pin.Image ||
			actual.ExecutableSHA256 != pin.ExecutableSHA256 {
			return stockReadProfile{}, ErrBoundary
		}
		destination := [...]int{0, 2, 1}[i]
		// Populate only from the two independently supplied expected sources.
		p.IDs[destination] = launch.ContainerID
		p.Images[destination] = pin.Image
		p.ExecutableSHA256[destination] = pin.ExecutableSHA256
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

func TestStockPilotJoinedIdentityRequiresIndependentInputs(t *testing.T) {
	p, _ := stockReadFixture()
	p.Directory = stockPilotHarnessDirectory
	pins := [3]stockPilotJoinedAssetPin{
		{"stock", p.Images[0], p.ExecutableSHA256[0]},
		{"ingress", p.Images[2], p.ExecutableSHA256[2]},
		{"gateway", p.Images[1], p.ExecutableSHA256[1]},
	}
	launched := [3]stockPilotJoinedLaunchID{
		{"stock", p.IDs[0]},
		{"ingress", p.IDs[2]},
		{"gateway", p.IDs[1]},
	}
	observed := [3]stockPilotFixtureIdentity{
		{"stock", p.IDs[0], p.Images[0], p.ExecutableSHA256[0]},
		{"ingress", p.IDs[2], p.Images[2], p.ExecutableSHA256[2]},
		{"gateway", p.IDs[1], p.Images[1], p.ExecutableSHA256[1]},
	}
	got, err := stockPilotFixtureProfileFromJoined(pins, launched, observed)
	if err != nil || got != p {
		t.Fatal("joined role mapping changed identity")
	}
	type testInput struct {
		pins     [3]stockPilotJoinedAssetPin
		launched [3]stockPilotJoinedLaunchID
		observed [3]stockPilotFixtureIdentity
	}
	for _, tt := range []struct {
		name  string
		alter func(*testInput)
	}{
		{"observed role order", func(in *testInput) { in.observed[1], in.observed[2] = in.observed[2], in.observed[1] }},
		{"reviewed role order", func(in *testInput) { in.pins[1], in.pins[2] = in.pins[2], in.pins[1] }},
		{"launch role order", func(in *testInput) { in.launched[1], in.launched[2] = in.launched[2], in.launched[1] }},
		{"missing role", func(in *testInput) { in.observed[1] = stockPilotFixtureIdentity{} }},
		{"prior run container", func(in *testInput) { in.observed[1].ContainerID = strings.Repeat("2", 64) }},
		{"different image", func(in *testInput) { in.observed[1].Image = "sha256:" + strings.Repeat("2", 64) }},
		{"different executable", func(in *testInput) { in.observed[2].ExecutableSHA256 = strings.Repeat("2", 64) }},
		{"matching but short ID", func(in *testInput) {
			in.launched[1].ContainerID = in.launched[1].ContainerID[:12]
			in.observed[1].ContainerID = in.launched[1].ContainerID
		}},
		{"matching but duplicate ID", func(in *testInput) {
			in.launched[1].ContainerID = in.launched[0].ContainerID
			in.observed[1].ContainerID = in.launched[0].ContainerID
		}},
		{"matching but wrong stock image", func(in *testInput) {
			in.pins[0].Image = "sha256:" + strings.Repeat("2", 64)
			in.observed[0].Image = in.pins[0].Image
		}},
		{"matching but wrong stock executable", func(in *testInput) {
			in.pins[0].ExecutableSHA256 = strings.Repeat("2", 64)
			in.observed[0].ExecutableSHA256 = in.pins[0].ExecutableSHA256
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := testInput{pins, launched, observed}
			tt.alter(&in)
			got, err := stockPilotFixtureProfileFromJoined(in.pins, in.launched, in.observed)
			if err != ErrBoundary || got != (stockReadProfile{}) {
				t.Fatal("unsafe or incomplete joined evidence produced a profile")
			}
		})
	}
}
