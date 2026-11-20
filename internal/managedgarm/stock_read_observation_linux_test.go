package managedgarm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Synthetic descriptions only. No test can inject success into the actual
// protected observer; none opens Docker, a namespace or reviewed profile file.
func stockReadFixture() (stockReadProfile, [3]container) {
	p := stockReadProfile{Version: "stock-readonly-profile/v1", Reference: stockReadReference, Platform: "linux/amd64", Directory: "/owned-pilot", IDs: [3]string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)}, Images: [3]string{stockReadImage, "sha256:" + strings.Repeat("d", 64), "sha256:" + strings.Repeat("e", 64)}, ExecutableSHA256: [3]string{stockReadExecutableHash, strings.Repeat("f", 64), strings.Repeat("1", 64)}}
	var c [3]container
	cmd := stockReadCommands()
	mounts := stockReadMounts(p)
	for i := range c {
		v := &c[i]
		v.ID = p.IDs[i]
		v.Image = p.Images[i]
		v.State.Running = true
		v.State.Pid = 100 + i
		v.Config.User = []string{"65530:65530", "65532:65532", "65531:65531"}[i]
		v.Config.WorkingDir = "/"
		v.Config.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
		v.HostConfig.ReadonlyRootfs = true
		v.HostConfig.CapDrop = []string{"ALL"}
		v.HostConfig.SecurityOpt = []string{"no-new-privileges:true"}
		v.HostConfig.NetworkMode = "container:" + p.IDs[0]
		v.Path = cmd[i][0]
		v.Args = cmd[i][1:]
		if i == 0 {
			v.HostConfig.NetworkMode = "none"
			v.Path = "/bin/sh"
			v.Args = []string{"-ec", `umask 077; exec "$1" -config /run/garm-private/config.toml`, "probe", "/bin/garm"}
			v.Config.Entrypoint = []string{"/bin/sh"}
			v.Config.Cmd = v.Args
		} else if i == 1 {
			v.Config.Entrypoint = cmd[i][:1]
			v.Config.Cmd = cmd[i][1:]
		} else {
			v.Config.Cmd = cmd[i]
		}
		for dst, src := range mounts[i] {
			v.Mounts = append(v.Mounts, struct {
				Source, Destination string
				RW                  bool
			}{src, dst, dst == "/var/lib/garm" || dst == "/var/lib/capacity-gateway"})
		}
	}
	return p, c
}
func TestStockReadPureProfileOnly(t *testing.T) {
	p, c := stockReadFixture()
	if validateStockReadContainers(p, c) != nil {
		t.Fatal("synthetic profile rejected")
	}
	p.ExecutableSHA256[1] = ""
	if validateStockReadContainers(p, c) == nil {
		t.Fatal("invented missing pin")
	}
}
func TestStockReadTopologyNegatives(t *testing.T) {
	cases := map[string]func(*stockReadProfile, *[3]container){
		"other stock": func(p *stockReadProfile, c *[3]container) {
			p.Images[0] = "sha256:" + strings.Repeat("9", 64)
			c[0].Image = p.Images[0]
		},
		"wrong launcher":  func(_ *stockReadProfile, c *[3]container) { c[0].Args = []string{"-c", "garm"} },
		"old layout":      func(_ *stockReadProfile, c *[3]container) { c[0].Path = "/usr/bin/garm" },
		"root user":       func(_ *stockReadProfile, c *[3]container) { c[0].Config.User = "0:0" },
		"shared pid":      func(_ *stockReadProfile, c *[3]container) { c[1].HostConfig.PidMode = "container:x" },
		"host network":    func(_ *stockReadProfile, c *[3]container) { c[0].HostConfig.NetworkMode = "host" },
		"missing mount":   func(_ *stockReadProfile, c *[3]container) { c[2].Mounts = nil },
		"foreign source":  func(_ *stockReadProfile, c *[3]container) { c[2].Mounts[0].Source = "/foreign" },
		"writable secret": func(_ *stockReadProfile, c *[3]container) { c[2].Mounts[0].RW = true },
		"duplicate role":  func(_ *stockReadProfile, c *[3]container) { c[1].ID = c[0].ID },
		"extra env":       func(_ *stockReadProfile, c *[3]container) { c[0].Config.Env = append(c[0].Config.Env, "LD_PRELOAD=x") },
		"capability":      func(_ *stockReadProfile, c *[3]container) { c[0].HostConfig.CapAdd = []string{"SYS_ADMIN"} },
		"privileged":      func(_ *stockReadProfile, c *[3]container) { c[0].HostConfig.Privileged = true },
		"cwd":             func(_ *stockReadProfile, c *[3]container) { c[0].Config.WorkingDir = "/tmp" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			p, c := stockReadFixture()
			change(&p, &c)
			if validateStockReadContainers(p, c) == nil {
				t.Fatal("accepted drift")
			}
		})
	}
}
func TestStockReadExactListeners(t *testing.T) {
	header := "sl local_address rem_address st\n"
	rows := header
	for i, p := range []int{8443, 9443, 9444, 9998} {
		rows += fmt.Sprintf("%d: 0100007F:%04X 00000000:0000 0A\n", i, p)
	}
	if !stockReadListeners([2][]byte{[]byte(rows), []byte(header)}) {
		t.Fatal("exact set rejected")
	}
	for _, bad := range []string{header, strings.ReplaceAll(rows, "0100007F", "00000000"), rows + "4: 0100007F:24E5 00000000:0000 0A\n", rows + "4: 0100007F:270E 00000000:0000 0A\n"} {
		if stockReadListeners([2][]byte{[]byte(bad), []byte(header)}) {
			t.Fatal("bad listener accepted")
		}
	}
	if stockReadListeners([2][]byte{[]byte(rows), []byte(header + "0: 00000000000000000000000001000000:20FB 0:0 0A\n")}) {
		t.Fatal("IPv6 accepted")
	}
}
func TestStockReadProcessStatus(t *testing.T) {
	raw := "Uid: 65530 65530 65530 65530\nGid: 65530 65530 65530 65530\nNoNewPrivs: 1\n"
	for _, k := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
		raw += k + ": 0000000000000000\n"
	}
	if !stockReadStatus([]byte(raw), "65530") {
		t.Fatal("status rejected")
	}
	for _, bad := range []string{raw + "Uid: 65530\n", strings.ReplaceAll(raw, "0000000000000000", "0000000000000001"), strings.ReplaceAll(raw, "NoNewPrivs: 1", "NoNewPrivs: 0")} {
		if stockReadStatus([]byte(bad), "65530") {
			t.Fatal("bad process accepted")
		}
	}
}
func TestStockReadCanceledDoesNoIO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := observeStockReadOnly(ctx); err == nil {
		t.Fatal("canceled admitted")
	}
	if _, err := observeStockReadOnly(nil); err == nil {
		t.Fatal("nil admitted")
	}
}

// Filesystem-only regression: no Docker or successful observation hook. The
// nonblocking open also protects a regular-to-FIFO replacement after lstat.
func TestStockReadFilesRefuseFIFO(t *testing.T) {
	rootDir := t.TempDir()
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"reviewed-profile.json", "executable"} {
		if err := syscall.Mkfifo(filepath.Join(rootDir, name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() {
		if _, _, err := stockReadProfileFile(root); err == nil {
			done <- fmt.Errorf("profile FIFO accepted")
			return
		}
		if _, err := stockReadExecutable(root, "executable", strings.Repeat("a", 64)); err == nil {
			done <- fmt.Errorf("executable FIFO accepted")
			return
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO read blocked")
	}
}
