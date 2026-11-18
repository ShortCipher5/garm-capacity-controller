//go:build linux

package protectedinit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
)

const ProtectedInitPath = "/usr/lib/garm-capacity/protected-init"
const ProtectedBundlePath = "/opt/actions-runner"
const ProtectedRuntimePath = "/run/garm-capacity/runner"
const ProtectedJournalParent = "/var/lib/garm-capacity"

func privateBytes(path string, max int64) ([]byte, error) {
	f, e := rootFile(path, false)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || st.Mode().Perm() != 0600 {
		return nil, ErrRefused
	}
	b, e := io.ReadAll(io.LimitReader(f, max+1))
	if e != nil || int64(len(b)) > max {
		return nil, ErrRefused
	}
	return b, nil
}
func parseBoot(manifest, jit []byte) (protectedboot.Manifest, protectedboot.Payload, error) {
	var m protectedboot.Manifest
	d := json.NewDecoder(bytes.NewReader(manifest))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF {
		return m, protectedboot.Payload{}, ErrRefused
	}
	p := protectedboot.Payload{Version: m.Version, RunnerName: m.RunnerName, Image: m.Image, JIT: string(jit)}
	raw, e := json.Marshal(p)
	if e != nil {
		return m, p, ErrRefused
	}
	p, e = protectedboot.Decode(raw)
	if e != nil {
		return m, p, ErrRefused
	}
	files, e := protectedboot.Materialize(p, protectedboot.Binding{DomainID: m.DomainID, PolicyDigest: m.PolicyDigest, ResourceID: m.ResourceID, ImageFingerprint: m.ImageFingerprint})
	if e != nil || !bytes.Equal(files[0].Data, manifest) || !bytes.Equal(files[1].Data, jit) {
		return m, p, ErrRefused
	}
	return m, p, nil
}
func selfMatches(d protectedboot.ImageDescriptor) bool {
	// The same pinned binary supplies PID1 and the post-drop fixed-argv helper.
	if d.InitSHA256 != d.LauncherSHA256 {
		return false
	}
	installed, e := rootFile(ProtectedInitPath, true)
	if e != nil {
		return false
	}
	defer installed.Close()
	running, e := os.Open("/proc/self/exe")
	if e != nil {
		return false
	}
	defer running.Close()
	a, e := installed.Stat()
	if e != nil {
		return false
	}
	b, e := running.Stat()
	if e != nil || !os.SameFile(a, b) {
		return false
	}
	h := sha256.New()
	if _, e = io.Copy(h, running); e != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == d.InitSHA256
}

// Boot connects the actual delivered manifest/JIT to root preparation and the
// non-root listener. It never consumes cloud-init or assumes cwd changes runner
// root. It is not wired by the native owner command until the image is qualified.
// Root-owned delivered files are trusted only within the separately qualified
// owner/image boot boundary; their contents alone are not authentication.
func Boot() error {
	if os.Getpid() != 1 || os.Geteuid() != 0 {
		return ErrRefused
	}
	raw, e := privateBytes(protectedboot.ManifestPath, 1<<20)
	if e != nil {
		return e
	}
	jit, e := privateBytes(protectedboot.JITPath, 1<<20)
	if e != nil {
		return e
	}
	manifest, payload, e := parseBoot(raw, jit)
	if e != nil || !selfMatches(payload.Image) {
		return ErrRefused
	}
	// No sudo/DHCP shell fallback. The trusted image/kernel network recipe must
	// establish IPv4 and root-controlled DNS before this listener is allowed out.
	if !networkPrepared() {
		return ErrRefused
	}
	marker, e := rootFile(filepath.Join(ProtectedJournalParent, ".protected-runner-layout"), false)
	if e != nil {
		return e
	}
	marker.Close()
	journal := filepath.Join(ProtectedJournalParent, manifest.ResourceID)
	if e = os.Mkdir(journal, 0700); e != nil {
		return ErrRefused
	}
	parent, e := os.Open(ProtectedJournalParent)
	if e != nil {
		return ErrRefused
	}
	e = parent.Sync()
	parent.Close()
	if e != nil {
		return ErrRefused
	}
	record := Record{Domain: manifest.DomainID, Incarnation: manifest.ResourceID, LauncherSHA256: payload.Image.InitSHA256, State: "launch_unknown", At: time.Now().UTC(), ExitCode: -1}
	if e = writeRecord(journal, record); e != nil {
		return ErrRefused
	}
	layout, e := PrepareRunnerLayout(RunnerLayoutSpec{Bundle: ProtectedBundlePath, Destination: ProtectedRuntimePath, JITFile: protectedboot.JITPath, RunnerName: payload.RunnerName, UID: payload.Image.UID, GID: payload.Image.GID, Files: payload.Image.Files})
	if e != nil {
		return ErrRefused
	}
	defer layout.Close()
	// Recheck descriptor identity after setup, before starting unprivileged code.
	st, e := layout.listener.Stat()
	if e != nil || st.Mode().Perm()&0111 == 0 {
		return ErrRefused
	}
	h := sha256.New()
	if _, e = io.Copy(h, layout.listener); e != nil || hex.EncodeToString(h.Sum(nil)) != payload.Image.Files["bin/Runner.Listener"] {
		return ErrRefused
	}
	if _, e = layout.listener.Seek(0, 0); e != nil {
		return ErrRefused
	}
	sig := make(chan os.Signal, 16)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sig)
	go func() {
		for range sig {
		}
	}()
	null, e := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if e != nil {
		return ErrRefused
	}
	defer null.Close()
	child, e := os.StartProcess("/proc/self/exe", []string{"protected-init", "--runner-child"}, &os.ProcAttr{Dir: layout.root, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + layout.root, "LANG=C.UTF-8", "DOTNET_MULTILEVEL_LOOKUP=0"}, Files: []*os.File{null, null, null, layout.listener}, Sys: &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: payload.Image.UID, Gid: payload.Image.GID, Groups: []uint32{}}, Setsid: true}})
	if e != nil {
		return ErrRefused
	}
	pid := child.Pid
	child.Release()
	code, e := reap(pid)
	if e != nil {
		return ErrRefused
	}
	if e = closeGuestNetwork(); e != nil {
		return ErrRefused
	}
	record.GuestNetworkClosed = true
	record.State = "process_tree_exited_unqualified"
	record.At = time.Now().UTC()
	record.ExitCode = code
	if e = writeRecord(journal, record); e != nil {
		return ErrRefused
	}
	return nil
}
func networkPrepared() bool {
	nic, e := net.InterfaceByName("eth0")
	if e != nil || nic.Flags&net.FlagUp == 0 {
		return false
	}
	addrs, e := nic.Addrs()
	if e != nil {
		return false
	}
	hasIPv4 := false
	for _, a := range addrs {
		ip, _, e := net.ParseCIDR(a.String())
		if e == nil && ip.To4() != nil && !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsLinkLocalUnicast() {
			hasIPv4 = true
		}
	}
	if !hasIPv4 {
		return false
	}
	f, e := rootFile("/etc/resolv.conf", false)
	if e != nil {
		return false
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 16385))
	if e != nil || len(b) > 16384 {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "nameserver" {
			ip := net.ParseIP(fields[1])
			if ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() {
				return true
			}
		}
	}
	return false
}
