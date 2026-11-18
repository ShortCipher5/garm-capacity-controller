//go:build linux

package nativecapsule

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type provisioningRecord struct {
	Version int    `json:"version"`
	BirthID string `json:"birth_id"`
	Digest  string `json:"digest"`
}

// BeginProvisioning is a permanent durable latch before any Incus resource IO.
// O_EXCL also fences copied Running values; there is no retry or adoption API.
func (r *Running) BeginProvisioning(digest string) error  { return r.provisionRecord(digest, false) }
func (r *Running) FinishProvisioning(digest string) error { return r.provisionRecord(digest, true) }
func (r *Running) provisionRecord(digest string, complete bool) error {
	if r == nil || r.staged == nil || r.staged.peer == nil || !hash(digest) || !r.staged.peer.Executable(r.nativeSHA) {
		return ErrCapsule
	}
	if writeProvisionRecord(r.staged.directory, provisioningRecord{1, r.BirthID(), digest}, complete) != nil || !r.staged.peer.Executable(r.nativeSHA) {
		return ErrCapsule
	}
	return nil
}
func writeProvisionRecord(directory string, rec provisioningRecord, complete bool) error {
	raw, _ := json.Marshal(rec)
	name := "provision-attempt.json"
	if complete {
		prior, e := os.ReadFile(filepath.Join(directory, name))
		if e != nil || string(prior) != string(raw) {
			return ErrCapsule
		}
		name = "provision-complete.json"
	}
	f, e := os.OpenFile(filepath.Join(directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return ErrCapsule
	}
	n, e := f.Write(raw)
	se := f.Sync()
	ce := f.Close()
	if e != nil || n != len(raw) || se != nil || ce != nil || syncDir(directory) != nil {
		return ErrCapsule
	}
	return nil
}
