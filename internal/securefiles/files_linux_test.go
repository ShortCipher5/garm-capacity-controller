package securefiles

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPrivateRegularFilesOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	if err := os.WriteFile(path, []byte("disposable-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := Read(path, 100)
	if err != nil || string(data) != "disposable-secret" {
		t.Fatal("private file unreadable")
	}
	if _, err := Read(path, 2); err == nil {
		t.Fatal("oversize file accepted")
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path, 100); err == nil {
		t.Fatal("group-readable secret accepted")
	}
	_ = os.Chmod(path, 0600)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(link, 100); err == nil {
		t.Fatal("secret symlink accepted")
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(fifo, 100); err == nil {
		t.Fatal("fifo secret accepted")
	}
	if _, err := Read(dir, 100); err == nil {
		t.Fatal("directory secret accepted")
	}
	if _, err := LoadTLS(TLSFiles{CA: path, Certificate: path, Key: path}); err == nil {
		t.Fatal("invalid certificate accepted")
	}
}
