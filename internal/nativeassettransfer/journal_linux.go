//go:build linux

package nativeassettransfer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassets"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

// Transfer consumes the ID durably before contacting the host. All failures
// after that point require explicit Inspect; neither restart nor a new review
// permits uploading the same ID again. The journal must be dedicated, protected
// deployment state, shared by every process using this credential/target scope.
func (b *Bundle) Transfer(ctx context.Context, p Plan, review string, key ssh.Signer, journal string) (nativeassets.Receipt, error) {
	return b.run(ctx, p, review, key, journal, false)
}

// Inspect performs only the fixed remote read-back operation, never an upload.
func (b *Bundle) Inspect(ctx context.Context, p Plan, review string, key ssh.Signer, journal string) (nativeassets.Receipt, error) {
	return b.run(ctx, p, review, key, journal, true)
}
func (b *Bundle) run(ctx context.Context, p Plan, review string, key ssh.Signer, journal string, inspect bool) (nativeassets.Receipt, error) {
	return b.runDial(ctx, p, review, key, journal, inspect, (&net.Dialer{Timeout: 5 * time.Second}).DialContext)
}
func (b *Bundle) runDial(ctx context.Context, p Plan, review string, key ssh.Signer, journal string, inspect bool, dial func(context.Context, string, string) (net.Conn, error)) (nativeassets.Receipt, error) {
	zero := nativeassets.Receipt{}
	if ctx == nil || ctx.Err() != nil || validate(p) != nil || review != Digest(p) || key == nil || ssh.FingerprintSHA256(key.PublicKey()) != p.Target.AdminKeyFingerprint {
		return zero, ErrInvalid
	}
	actual, e := b.Plan(p.Target)
	if e != nil || Digest(actual) != review {
		return zero, ErrInvalid
	}
	uid := uint32(os.Geteuid())
	if protected(journal, uid) != nil {
		return zero, ErrInvalid
	}
	st, e := os.Lstat(journal)
	if e != nil || st.Mode().Perm() != 0700 || st.Sys().(*syscall.Stat_t).Uid != uid {
		return zero, ErrInvalid
	}
	root, e := os.OpenRoot(journal)
	if e != nil {
		return zero, ErrInvalid
	}
	defer root.Close()
	// The exclusive immutable intent itself serializes every writer. There is no
	// reset, deletion or ambiguous retry operation and no mutable lock lifecycle.
	raw, _ := json.Marshal(p)
	name := p.Manifest.ID + ".intent.json"
	if inspect {
		f, e := root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if e != nil {
			return zero, ErrUnknown
		}
		defer f.Close()
		st, e := f.Stat()
		if e != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
			return zero, ErrUnknown
		}
		ss, ok := st.Sys().(*syscall.Stat_t)
		if !ok || ss.Uid != uid || ss.Nlink != 1 || st.Size() > 65536 {
			return zero, ErrUnknown
		}
		old, e := io.ReadAll(io.LimitReader(f, 65537))
		if e != nil || !bytes.Equal(old, raw) {
			return zero, ErrUnknown
		}
	} else {
		f, e := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0600)
		if e != nil {
			return zero, ErrUnknown
		}
		e = f.Chmod(0600)
		if e == nil {
			_, e = f.Write(raw)
		}
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil || ce != nil {
			return zero, ErrUnknown
		}
		d, e := os.Open(filepath.Clean(journal))
		if e != nil {
			return zero, ErrUnknown
		}
		e = d.Sync()
		d.Close()
		if e != nil {
			return zero, ErrUnknown
		}
	}
	return b.execute(ctx, p, review, key, inspect, dial)
}
