package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"golang.org/x/sys/unix"
)

const snapshotName = "ci-health-history-v1.json"

func testDirectory(t *testing.T) (cihealth.HistoryDirectoryReview, []string) {
	t.Helper()
	workspace, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, parent := range []string{t.TempDir(), workspace} {
		dir, err := os.MkdirTemp(parent, ".ci-history-init-")
		if err != nil {
			continue
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		stat := info.Sys().(*syscall.Stat_t)
		pin := cihealth.HistoryDirectoryReview{Directory: dir, Device: uint64(stat.Dev), Inode: stat.Ino}
		root, err := cihealth.OpenReviewedHistoryDirectory(t.Context(), pin)
		if err != nil {
			continue
		}
		root.Close()
		return pin, []string{"--initialize-empty-store", dir, strconv.FormatUint(pin.Device, 10), strconv.FormatUint(pin.Inode, 10)}
	}
	t.Skip("test requires a real private directory under trusted ancestors")
	return cihealth.HistoryDirectoryReview{}, nil
}

func TestCLIInitializesExactlyOnceAndReopensOnlyThroughSeparateReview(t *testing.T) {
	pin, args := testDirectory(t)
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), args, &stdout, &stderr); code != 0 || stderr.Len() != 0 ||
		!strings.Contains(stdout.String(), "Independent startup review is still required") || strings.Contains(stdout.String(), pin.Directory) {
		t.Fatalf("unexpected initialization acknowledgment: %d", code)
	}
	raw, err := os.ReadFile(filepath.Join(pin.Directory, snapshotName))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"Format":"garm-ci-health-history-v1","Checkpoints":[],"Attempts":[]}` {
		t.Fatal("initializer produced a nonempty or noncanonical snapshot")
	}
	entries, err := os.ReadDir(pin.Directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != snapshotName {
		t.Fatalf("unexpected startup slot contents: %v", err)
	}
	info, err := entries[0].Info()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("initial snapshot is not private")
	}
	root, err := cihealth.OpenReviewedHistoryDirectory(t.Context(), pin)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	scope := cihealth.HistoryScope{Source: "fixture", RepositoryID: 1, WorkflowID: 2}
	h, err := cihealth.OpenHistory(t.Context(), cihealth.HistoryInput{Enabled: true, Root: root, Review: &cihealth.HistoryReopenReview{
		SnapshotSHA256: sha256.Sum256(raw), AllowedScopes: []cihealth.HistoryScope{scope},
	}})
	if err != nil {
		t.Fatal("initializer did not leave usable exact reviewed bytes", err)
	}
	cursor, err := h.Cursor(t.Context(), scope)
	if err != nil || cursor != (cihealth.HistoryCursor{}) || h.Close() != nil {
		t.Fatal("selected future workflow did not begin at the empty cursor", err)
	}
	stdout.Reset()
	if code := run(t.Context(), args, &stdout, &stderr); code != 1 || stdout.Len() != 0 {
		t.Fatal("existing snapshot was accepted", code)
	}
	after, err := os.ReadFile(filepath.Join(pin.Directory, snapshotName))
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("repeated initialization changed the existing snapshot", err)
	}
}

func TestCLIRefusesWrongIdentityAliasesExtraEntriesAndHeldWriters(t *testing.T) {
	for _, kind := range []string{"device", "inode", "symlink", "loose-mode", "unrelated", "intent", "stage", "lock", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			pin, args := testDirectory(t)
			ctx := t.Context()
			var existing string
			switch kind {
			case "device":
				args[2] = strconv.FormatUint(pin.Device+1, 10)
			case "inode":
				args[3] = strconv.FormatUint(pin.Inode+1, 10)
			case "symlink":
				args[1] = filepath.Join(pin.Directory, "alias")
				if err := os.Symlink(pin.Directory, args[1]); err != nil {
					t.Fatal(err)
				}
			case "loose-mode":
				if err := os.Chmod(pin.Directory, 0750); err != nil {
					t.Fatal(err)
				}
			case "unrelated", "intent", "stage":
				existing = "unrelated"
				if kind != "unrelated" {
					existing = "ci-health-history-v1." + kind
				}
				if err := os.WriteFile(filepath.Join(pin.Directory, existing), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			case "lock":
				d, err := os.Open(pin.Directory)
				if err != nil {
					t.Fatal(err)
				}
				defer d.Close()
				if err := unix.Flock(int(d.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			var stdout, stderr bytes.Buffer
			if code := run(ctx, args, &stdout, &stderr); code != 1 || stdout.Len() != 0 ||
				stderr.Len() == 0 || strings.Contains(stderr.String(), pin.Directory) {
				t.Fatal("unsafe input was accepted or disclosed", code)
			}
			if _, err := os.Lstat(filepath.Join(pin.Directory, snapshotName)); !os.IsNotExist(err) {
				t.Fatal("refusal created history", err)
			}
			if existing != "" {
				after, err := os.ReadFile(filepath.Join(pin.Directory, existing))
				if err != nil || string(after) != "preserve" {
					t.Fatal("refusal cleaned or changed the existing entry", err)
				}
			}
		})
	}
}

type failOutput struct{}

func (failOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCLIMalformedArgumentsAndOutputFailure(t *testing.T) {
	pin, args := testDirectory(t)
	for _, raw := range []string{"0", "+1", "01", "-1", "0x1", "18446744073709551616"} {
		bad := append([]string(nil), args...)
		bad[2] = raw
		if code := run(t.Context(), bad, io.Discard, io.Discard); code != 2 {
			t.Fatalf("noncanonical device accepted: %q", raw)
		}
	}
	if code := run(t.Context(), args[1:], io.Discard, io.Discard); code != 2 {
		t.Fatal("initialization intent was not required")
	}
	var stderr bytes.Buffer
	if code := run(t.Context(), args, failOutput{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "was initialized") {
		t.Fatal("lost acknowledgment misreported the state", code)
	}
	if _, err := os.Lstat(filepath.Join(pin.Directory, snapshotName)); err != nil {
		t.Fatal("output failure rolled back the already initialized store", err)
	}
}
