package firstboot

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"syscall"
)

type RotationOperation struct {
	Digest   string           `json:"review_digest"`
	State    string           `json:"state"`
	Attempts int              `json:"attempts"`
	Receipt  *RotationReceipt `json:"receipt,omitempty"`
}

// JournalRotation uses a separately provisioned owner-private state directory.
// Its ancestor directories, like credential-file ancestors, must be trusted.
// It holds an OS lock and persists unknown before invoking any remote effect.
func JournalRotation(ctx context.Context, directory string, r RotationRequest, retry bool, execute func(context.Context) (RotationReceipt, error)) (RotationOperation, error) {
	if ValidateRotation(r) != nil || execute == nil {
		return RotationOperation{}, ErrInvalid
	}
	st, e := os.Lstat(directory)
	if e != nil || !st.IsDir() || st.Mode().Perm() != 0700 || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return RotationOperation{}, ErrInvalid
	}
	root, e := os.OpenRoot(directory)
	if e != nil {
		return RotationOperation{}, e
	}
	defer root.Close()
	lock, e := root.OpenFile("rotation.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return RotationOperation{}, e
	}
	defer lock.Close()
	private := func(f *os.File) bool {
		s, e := f.Stat()
		return e == nil && s.Mode().IsRegular() && s.Mode().Perm() == 0600 && s.Sys().(*syscall.Stat_t).Uid == uint32(os.Geteuid())
	}
	if !private(lock) || syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return RotationOperation{}, ErrState
	}
	name := r.Plan.RequestID + ".json"
	op := RotationOperation{Digest: r.ReviewDigest, State: "unknown"}
	f, e := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if e == nil {
		if !private(f) {
			f.Close()
			return op, ErrState
		}
		data, err := io.ReadAll(io.LimitReader(f, 16385))
		f.Close()
		if err != nil || len(data) > 16384 {
			return op, ErrState
		}
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if d.Decode(&op) != nil || d.Decode(new(any)) != io.EOF || op.Digest != r.ReviewDigest || op.Attempts < 1 || op.Attempts > 100 {
			return op, ErrState
		}
		if op.State == "revoked" {
			commit := r
			commit.Phase = "commit"
			if op.Receipt == nil || validateRotationReceipt(commit, *op.Receipt) != nil {
				return op, ErrState
			}
			return op, nil
		}
		if op.State != "unknown" || op.Receipt != nil || !retry {
			return op, ErrState
		}
	} else if !os.IsNotExist(e) || retry {
		return op, ErrState
	}
	if op.Attempts >= 100 {
		return op, ErrState
	}
	op.Attempts++
	persist := func() error {
		data, e := json.Marshal(op)
		if e != nil {
			return e
		}
		var random [16]byte
		if _, e = rand.Read(random[:]); e != nil {
			return e
		}
		tmp := ".rotation-" + hex.EncodeToString(random[:])
		f, e := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		defer root.Remove(tmp)
		if _, e = f.Write(data); e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		if e = root.Rename(tmp, name); e != nil {
			return e
		}
		dir, e := root.Open(".")
		if e != nil {
			return e
		}
		defer dir.Close()
		return dir.Sync()
	}
	if e = persist(); e != nil {
		return op, e
	}
	receipt, e := execute(ctx)
	if e != nil {
		return op, e
	}
	commit := r
	commit.Phase = "commit"
	if validateRotationReceipt(commit, receipt) != nil {
		return op, ErrState
	}
	op.State = "revoked"
	op.Receipt = &receipt
	if e = persist(); e != nil {
		return RotationOperation{Digest: op.Digest, State: "unknown", Attempts: op.Attempts}, e
	}
	return op, nil
}
