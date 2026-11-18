//go:build linux

package nativeassettransfer

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

type Configuration struct {
	BundleDirectory  string `json:"bundle_directory"`
	HelperPath       string `json:"helper_path"`
	ManifestSHA256   string `json:"manifest_sha256"`
	HelperSHA256     string `json:"helper_sha256"`
	JournalDirectory string `json:"journal_directory"`
	AdminKeyFile     string `json:"admin_key_file"`
	Target           Target `json:"target"`
}

func privateBytes(path string) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || protected(filepath.Dir(path), uint32(os.Geteuid())) != nil {
		return nil, ErrInvalid
	}
	f, e := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if e != nil {
		return nil, ErrInvalid
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || st.Size() < 1 || st.Size() > 65536 {
		return nil, ErrInvalid
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || s.Uid != uint32(os.Geteuid()) || s.Nlink != 1 {
		return nil, ErrInvalid
	}
	raw, e := io.ReadAll(io.LimitReader(f, 65537))
	if e != nil || len(raw) > 65536 {
		return nil, ErrInvalid
	}
	return raw, nil
}

// LoadConfiguration reads deployment-protected configuration, not browser paths.
// Requiring canonical JSON also rejects duplicate and case-aliased fields.
func LoadConfiguration(path string) (Configuration, error) {
	var c Configuration
	raw, e := privateBytes(path)
	if e != nil {
		return c, e
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return Configuration{}, ErrInvalid
	}
	canon, _ := json.Marshal(c)
	if !bytes.Equal(raw, canon) {
		return Configuration{}, ErrInvalid
	}
	return c, nil
}
func (c Configuration) LoadSigner() (ssh.Signer, error) {
	raw, e := privateBytes(c.AdminKeyFile)
	if e != nil {
		return nil, e
	}
	defer clear(raw)
	k, e := ssh.ParsePrivateKey(raw)
	if e != nil || ssh.FingerprintSHA256(k.PublicKey()) != c.Target.AdminKeyFingerprint {
		return nil, ErrInvalid
	}
	return k, nil
}
