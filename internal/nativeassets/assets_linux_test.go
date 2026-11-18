//go:build linux

package nativeassets

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func fixture(t *testing.T) (string, string, Manifest, string) {
	t.Helper()
	cwd, e := os.Getwd()
	if e != nil {
		t.Fatal(e)
	}
	root, e := os.MkdirTemp(cwd, ".assets-test-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	source, parent := filepath.Join(root, "source"), filepath.Join(root, "prepared")
	for _, p := range []string{source, parent} {
		if os.Mkdir(p, 0700) != nil {
			t.Fatal("mkdir")
		}
	}
	m := Manifest{Version, strings.Repeat("a", 64), "amd64", map[string]Entry{}}
	for _, n := range names {
		f, e := os.OpenFile(filepath.Join(source, n), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if e != nil {
			t.Fatal(e)
		}
		if n == "runtime.raw" {
			e = f.Truncate(64 << 20)
		} else {
			b := make([]byte, 64)
			copy(b, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
			binary.LittleEndian.PutUint16(b[16:], 2)
			binary.LittleEndian.PutUint16(b[18:], 62)
			binary.LittleEndian.PutUint32(b[20:], 1)
			binary.LittleEndian.PutUint16(b[52:], 64)
			_, e = f.Write(b)
			f.Seek(0, 0)
		}
		if e != nil {
			t.Fatal(e)
		}
		s, _ := f.Stat()
		h := sha256.New()
		if _, e = io.Copy(h, f); e != nil {
			t.Fatal(e)
		}
		f.Close()
		m.Files[n] = Entry{hex.EncodeToString(h.Sum(nil)), s.Size()}
	}
	raw, _ := json.Marshal(m)
	if os.WriteFile(filepath.Join(source, "manifest.json"), raw, 0600) != nil {
		t.Fatal("manifest")
	}
	d, e := ManifestDigest(m)
	if e != nil {
		t.Fatal(e)
	}
	return source, parent, m, d
}
func TestPrepareInspectNoReplayAndConcurrentOneUse(t *testing.T) {
	source, parent, m, d := fixture(t)
	uid := uint32(os.Getuid())
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := prepare(context.Background(), source, parent, d, uid)
			if e == nil && ValidateReceipt(m, r) != nil {
				e = ErrAssets
			}
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal("one-use", success)
	}
	got, e := inspect(context.Background(), parent, m.ID, d, uid)
	if e != nil || ValidateReceipt(m, got) != nil {
		t.Fatal(got, e)
	}
	if _, e = prepare(context.Background(), source, parent, d, uid); e == nil {
		t.Fatal("replayed")
	}
	path := filepath.Join(parent, m.ID, "ip")
	os.Chmod(path, 0600)
	changed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed[len(changed)-1] ^= 1
	if os.WriteFile(path, changed, 0600) != nil || os.Chmod(path, 0555) != nil {
		t.Fatal("tamper fixture")
	}
	if _, e = inspect(context.Background(), parent, m.ID, d, uid); e == nil {
		t.Fatal("tamper accepted")
	}
}
func TestPartialPreparationNeverAdopted(t *testing.T) {
	source, parent, m, d := fixture(t)
	uid := uint32(os.Getuid())
	path := filepath.Join(source, "systemd-nspawn")
	original, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(path, []byte("corrupt"), 0600)
	if _, e = prepare(context.Background(), source, parent, d, uid); e == nil {
		t.Fatal("corrupt accepted")
	}
	if _, e = os.Stat(filepath.Join(parent, m.ID, "intent.json")); e != nil {
		t.Fatal("intent missing", e)
	}
	os.WriteFile(path, original, 0600)
	if _, e = prepare(context.Background(), source, parent, d, uid); e == nil {
		t.Fatal("partial adopted")
	}
	if _, e = inspect(context.Background(), parent, m.ID, d, uid); e == nil {
		t.Fatal("partial called complete")
	}
}
func TestRejectManifestAndInputsBeforeEffects(t *testing.T) {
	source, parent, m, d := fixture(t)
	uid := uint32(os.Getuid())
	if _, e := prepare(context.Background(), source, parent, strings.Repeat("b", 64), uid); e == nil {
		t.Fatal("wrong review")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := prepare(ctx, source, parent, d, uid); e == nil {
		t.Fatal("cancel")
	}
	if _, e := os.Stat(filepath.Join(parent, m.ID)); !os.IsNotExist(e) {
		t.Fatal("unexpected effect", e)
	}
	m.Files["../escape"] = Entry{strings.Repeat("c", 64), 1}
	if Validate(m) == nil {
		t.Fatal("unknown path")
	}
	if os.Geteuid() != 0 {
		if _, e := Prepare(context.Background(), source, d); e == nil {
			t.Fatal("nonroot")
		}
	}
}

func TestHelperExportPreservesDefaultGatewayImage(t *testing.T) {
	raw, e := os.ReadFile("../../Dockerfile")
	if e != nil {
		t.Fatal(e)
	}
	stages := []string{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 4 && strings.EqualFold(fields[0], "FROM") && strings.EqualFold(fields[2], "AS") {
			stages = append(stages, fields[3])
		}
	}
	if len(stages) < 2 || stages[len(stages)-1] != "gateway" {
		t.Fatal("default image changed", stages)
	}
	helper := false
	for _, stage := range stages[:len(stages)-1] {
		if stage == "native-runtime-assets-helper" {
			helper = true
		}
	}
	if !helper {
		t.Fatal("separate export target missing")
	}
}
