//go:build linux

package nativeegress

import (
	"bytes"
	"context"
	"encoding/json"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type installRequest struct {
	Version   int           `json:"version"`
	BirthID   string        `json:"birth_id"`
	Namespace uint64        `json:"namespace"`
	Policy    Specification `json:"policy"`
}
type toolRun func(int, []string, []byte) ([]byte, error)

func runTool(fd int, args []string, input []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "/proc/self/fd/"+strconv.Itoa(fd), args...)
	c.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "LANG=C"}
	c.Dir = "/"
	c.Stdin = bytes.NewReader(input)
	var b boundedOutput
	c.Stdout = &b
	c.Stderr = io.Discard
	c.WaitDelay = time.Second
	if c.Run() != nil {
		return nil, ErrUnknown
	}
	return b.Bytes(), nil
}

// InstallHelper runs only as a pinned root subprocess. FD3 is the held capsule
// netns, FD4 pinned iproute2, FD5 pinned nft. No caller-supplied argv is executed.
func InstallHelper() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if os.Geteuid() != 0 || (len(os.Args) != 1 && !(len(os.Args) == 2 && os.Args[1] == "inspect")) {
		return ErrRefused
	}
	kind, e := unix.IoctlRetInt(3, unix.NS_GET_NSTYPE)
	var ns, host unix.Stat_t
	if e != nil || kind != unix.CLONE_NEWNET || unix.Fstat(3, &ns) != nil || unix.Stat("/proc/self/ns/net", &host) != nil || ns.Ino == host.Ino {
		return ErrRefused
	}
	for _, fd := range []int{4, 5} {
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0022 != 0 || st.Mode&0111 == 0 || st.Mode&07000 != 0 {
			return ErrRefused
		}
	}
	raw, e := io.ReadAll(io.LimitReader(os.Stdin, 65537))
	if e != nil || len(raw) > 65536 {
		return ErrRefused
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var r installRequest
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF || r.Version != 1 || r.Namespace != ns.Ino || !birth(r.BirthID) {
		return ErrRefused
	}
	canonical, _ := json.Marshal(r)
	if !bytes.Equal(raw, canonical) {
		return ErrRefused
	}
	p, e := Compile(r.Policy)
	if e != nil {
		return e
	}
	// All ambient supervisor descriptors disappear from executable tools. The
	// helper retains its own held descriptors; no process-wide parent scanning.
	if sanitizeToolDescriptors() != nil {
		return ErrRefused
	}
	hf, e := os.Open("/proc/self/ns/net")
	if e != nil {
		return ErrRefused
	}
	defer hf.Close()
	switchGuest := func() error { return unix.Setns(3, unix.CLONE_NEWNET) }
	switchHost := func() error { return unix.Setns(int(hf.Fd()), unix.CLONE_NEWNET) }
	raw, e = os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if e != nil || strings.TrimSpace(string(raw)) != "1" {
		return ErrRefused
	}
	if len(os.Args) == 1 {
		if install(p, r.BirthID, runTool, switchGuest, switchHost, func(name string) error { return createVeth(name, 3) }) != nil {
			return ErrUnknown
		}
	}
	obs, e := inspectNetwork(p, r.BirthID, runTool, switchGuest, switchHost)
	if e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(obs)
}
func install(p Policy, id string, run toolRun, guest, host func() error, create func(string) error) error {
	iface, _, e := p.names(id)
	if e != nil {
		return e
	}
	raw, e := run(5, []string{"-j", "list", "ruleset"}, nil)
	_, table, _ := p.names(id)
	if e != nil || !rulesetSafe(raw, table) {
		return ErrRefused
	}
	raw, e = run(4, []string{"-j", "address", "show"}, nil)
	if e != nil || !hostAddressesSafe(raw, p, iface) {
		return ErrRefused
	}
	raw, e = run(4, []string{"-4", "-j", "route", "show", "table", "all"}, nil)
	if e != nil || !hostRoutesSafe(raw, p) {
		return ErrRefused
	}
	if guest() != nil {
		return ErrUnknown
	}
	raw, e = run(4, []string{"-j", "link", "show"}, nil)
	back := host()
	if e != nil || back != nil || !onlyLoopback(raw) {
		return ErrRefused
	}
	fw, _ := p.firewall(id)
	bridge, _ := p.bridgeGuard(id)
	input := firewallInput(append(append([]object{}, fw...), bridge...))
	if _, e = run(5, []string{"-j", "-c", "-f", "-"}, input); e != nil {
		return ErrRefused
	}
	if _, e = run(5, []string{"-j", "-f", "-"}, input); e != nil {
		return ErrUnknown
	}
	verify := func() error {
		return verifyFirewalls(p, id, run)
	}
	if verify() != nil || create(iface) != nil {
		return ErrUnknown
	}
	if _, e = run(4, []string{"address", "add", p.HostAddress() + "/30", "dev", iface}, nil); e != nil {
		return ErrUnknown
	}
	if guest() != nil {
		return ErrUnknown
	}
	// Failure never silently resumes in the wrong namespace. This subprocess
	// exits; there is no attempted cleanup or re-use of uncertain network state.
	for _, a := range [][]string{{"address", "add", p.GuestAddress() + "/30", "dev", "garm0"}, {"link", "set", "dev", "garm0", "up"}, {"route", "add", "default", "via", p.HostAddress(), "dev", "garm0"}} {
		if _, e = run(4, a, nil); e != nil {
			return ErrUnknown
		}
	}
	if host() != nil || verify() != nil {
		return ErrUnknown
	}
	if _, e = run(4, []string{"link", "set", "dev", iface, "up"}, nil); e != nil {
		return ErrUnknown
	}
	return verify()
}

type observation struct{ Host, Guest linkObservation }

func inspectNetwork(p Policy, id string, run toolRun, guest, host func() error) (observation, error) {
	iface, _, e := p.names(id)
	if e != nil {
		return observation{}, e
	}
	if verifyFirewalls(p, id, run) != nil {
		return observation{}, ErrUnknown
	}
	raw, e := run(5, []string{"-j", "list", "ruleset"}, nil)
	if e != nil || !rulesetSafe(raw, "") {
		return observation{}, ErrUnknown
	}
	raw, e = run(4, []string{"-d", "-j", "address", "show", "dev", iface}, nil)
	if e != nil {
		return observation{}, ErrUnknown
	}
	h, e := observeLink(raw, iface, p.HostAddress())
	if e != nil {
		return observation{}, e
	}
	if guest() != nil {
		return observation{}, ErrUnknown
	}
	raw, e = run(4, []string{"-d", "-j", "address", "show", "dev", "garm0"}, nil)
	back := host()
	if e != nil || back != nil {
		return observation{}, ErrUnknown
	}
	g, e := observeLink(raw, "garm0", p.GuestAddress())
	if e != nil || h.Peer != g.Index || g.Peer != h.Index {
		return observation{}, ErrUnknown
	}
	return observation{h, g}, nil
}

func sanitizeToolDescriptors() error { return unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC) }

func verifyFirewalls(p Policy, id string, run toolRun) error {
	_, table, e := p.names(id)
	if e != nil {
		return e
	}
	inet, _ := p.firewall(id)
	bridge, _ := p.bridgeGuard(id)
	for _, family := range []string{"inet", "bridge"} {
		want := inet
		if family == "bridge" {
			want = bridge
		}
		raw, e := run(5, []string{"-j", "-nn", "list", "table", family, table}, nil)
		if e != nil || !sameFirewall(raw, want) {
			return ErrUnknown
		}
	}
	return nil
}
