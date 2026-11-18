//go:build linux

package nativeapi

import (
	"net/http"
	"testing"
)

func TestBootstrapMetadataRejectsAliasesAndWrongPermissions(t *testing.T) {
	h := http.Header{}
	for k, v := range map[string]string{"X-Incus-uid": "0", "X-Incus-gid": "0", "X-Incus-mode": "0600", "X-Incus-type": "file"} {
		h.Set(k, v)
	}
	if !rootFileHeaders(h) {
		t.Fatal("valid")
	}
	h.Add("X-Incus-uid", "0")
	if rootFileHeaders(h) {
		t.Fatal("duplicate")
	}
	h.Set("X-Incus-uid", "0")
	h.Set("X-Incus-mode", "0644")
	if rootFileHeaders(h) {
		t.Fatal("leaky")
	}
	h.Set("X-Incus-type", "directory")
	h.Set("X-Incus-mode", "0755")
	if !rootDirectory(h) {
		t.Fatal("directory")
	}
	h.Set("X-Incus-mode", "0777")
	if rootDirectory(h) {
		t.Fatal("writable parent")
	}
	h.Set("X-Incus-type", "symlink")
	h.Set("X-Incus-mode", "0700")
	if rootDirectory(h) {
		t.Fatal("symlink")
	}
}
func TestBootstrapZeroInputsNeverWrite(t *testing.T) {
	var c *Client
	if _, e := c.DeliverBootstrap(t.Context(), Prepared{}, ""); e == nil {
		t.Fatal("zero")
	}
	if (BootstrapFiles{}).Valid() {
		t.Fatal("zero receipt")
	}
}
