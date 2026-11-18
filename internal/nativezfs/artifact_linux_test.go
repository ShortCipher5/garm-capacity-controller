//go:build linux

package nativezfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestArtifactHelperGrammarIsFreshOnly(t *testing.T) {
	id := strings.Repeat("a", 64)
	good := []string{"create", "-u", "-o", "quota=1024", "-o", "mountpoint=legacy", "-o", "canmount=noauto", "-o", "zoned=off", "tank/root/control-" + id}
	if !validCommand(good) {
		t.Fatal("valid")
	}
	for _, change := range []func([]string){func(a []string) { a[3] = "quota=0" }, func(a []string) { a[3] = "quota=01" }, func(a []string) { a[9] = "zoned=on" }, func(a []string) { a[10] = "tank/root/existing" }, func(a []string) { a[10] += "/child" }, func(a []string) { a[1] = "-p" }} {
		a := append([]string(nil), good...)
		change(a)
		if validCommand(a) {
			t.Fatal(a)
		}
	}
}
func TestArtifactCopyPinsExactBoundedBytes(t *testing.T) {
	raw := []byte("reviewed immutable image")
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	var dst bytes.Buffer
	if n, e := copyArtifactBytes(context.Background(), bytes.NewReader(raw), &dst, int64(len(raw)), digest); e != nil || n != int64(len(raw)) || !bytes.Equal(dst.Bytes(), raw) {
		t.Fatal(n, e)
	}
	for _, input := range [][]byte{raw[:len(raw)-1], append(append([]byte(nil), raw...), 1), []byte(strings.Repeat("x", len(raw)))} {
		dst.Reset()
		if _, e := copyArtifactBytes(context.Background(), bytes.NewReader(input), &dst, int64(len(raw)), digest); e == nil {
			t.Fatal("changed input accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := copyArtifactBytes(ctx, bytes.NewReader(raw), &dst, int64(len(raw)), digest); e == nil {
		t.Fatal("cancel ignored")
	}
}
func TestArtifactObservationCannotBeFabricatedOrRevived(t *testing.T) {
	if (ArtifactRootObservation{}).Fresh() {
		t.Fatal("zero")
	}
	if (ArtifactRootObservation{at: time.Now().Add(-6 * time.Second)}).Fresh() {
		t.Fatal("stale")
	}
	root := HostArtifactRoot{state: &artifactState{}}
	copy := root
	root.Close()
	if _, e := copy.Observe(context.Background()); e == nil {
		t.Fatal("copy revived")
	}
	if _, e := (HostArtifactRoot{}).Paths(context.Background()); e == nil {
		t.Fatal("paths from empty root")
	}
}
func TestArtifactPathsFitBirthSocketAndPreserveGlobalRegistry(t *testing.T) {
	socket := ArtifactMount + "/state/" + strings.Repeat("a", 64) + "/bootstrap.sock"
	if len(socket) >= 104 {
		t.Fatal("Unix socket exceeds bound", len(socket))
	}
	if strings.Contains(ArtifactRegistry, ArtifactMount) {
		t.Fatal("global one-use latch hidden under new mount")
	}
}
func TestArtifactPropertiesRequireHostOnlyQuota(t *testing.T) {
	r := artifactRecord{GUID: "2", QuotaBytes: 1024}
	p := map[string]string{"guid": "2", "type": "filesystem", "quota": "1024", "zoned": "off", "mountpoint": "legacy", "canmount": "noauto"}
	if !artifactProperties(p, r) {
		t.Fatal("valid")
	}
	for k, v := range map[string]string{"guid": "3", "quota": "0", "zoned": "on", "mountpoint": "/host", "canmount": "on"} {
		old := p[k]
		p[k] = v
		if artifactProperties(p, r) {
			t.Fatal(k)
		}
		p[k] = old
	}
}

func TestArtifactMountRejectsEveryNestedRegistryEscape(t *testing.T) {
	root := "42 1 0:1 / " + ArtifactMount + " rw,nodev,nosuid,noexec - zfs tank/root/control-x rw\n"
	if !exactMount([]byte(root), ArtifactMount, "tank/root/control-x", 42) {
		t.Fatal("root projection")
	}
	for _, name := range append(append([]string{}, artifactDirectories...), "images/runtime.raw") {
		extra := "43 42 0:2 / " + ArtifactMount + "/" + name + " rw,nodev,nosuid,noexec - tmpfs tmpfs rw\n"
		if exactMount([]byte(root+extra), ArtifactMount, "tank/root/control-x", 42) {
			t.Fatal("nested quota escape accepted", name)
		}
	}
	if artifactPathMount("/definitely/missing/artifact", 42) || artifactPathMount("/", 0) {
		t.Fatal("fabricated mount identity")
	}
}

func TestArtifactConfigurationPreflightPinsEveryTool(t *testing.T) {
	c := ArtifactConfiguration{Binary: "/usr/sbin/zfs", BinarySHA256: strings.Repeat("a", 64), Helper: "/usr/libexec/zfs", HelperSHA256: strings.Repeat("b", 64), ParentDataset: "tank/artifacts", ParentGUID: "1", QuotaBytes: 1 << 30, Image: "/image.raw", ImageSHA256: strings.Repeat("c", 64)}
	if ValidateArtifactConfiguration(c) != nil {
		t.Fatal("valid shape")
	}
	for _, change := range []func(*ArtifactConfiguration){
		func(c *ArtifactConfiguration) { c.Binary = "zfs" },
		func(c *ArtifactConfiguration) { c.Helper = "/usr/../helper" },
		func(c *ArtifactConfiguration) { c.BinarySHA256 = "" },
		func(c *ArtifactConfiguration) { c.HelperSHA256 = "" },
		func(c *ArtifactConfiguration) { c.ParentGUID = "0" },
		func(c *ArtifactConfiguration) { c.QuotaBytes = 0 },
	} {
		x := c
		change(&x)
		if ValidateArtifactConfiguration(x) == nil {
			t.Fatal("invalid shape")
		}
	}
}
