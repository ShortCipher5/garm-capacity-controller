package managedgarm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Root-reviewed public commitments, not authentication or a bootstrap request.
// All five files live in <reviewed pilot Directory>/supervisor-admin.
type stockReadAuthReview struct {
	Version, GarmID, LeafSHA256, AttemptSHA256, RecordSHA256 string
}
type stockReadAuthBinding struct {
	review stockReadAuthReview
	record bootstrapRecord
}

func stockBindingDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func stockBindingJSON(raw []byte, out any) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return false
	}
	// A single canonical encoding also rejects duplicate and case-aliased keys.
	canonical, err := json.Marshal(out)
	return err == nil && bytes.Equal(raw, canonical)
}
func validateStockReadAuthBinding(garmID string, raw [5][]byte) (stockReadAuthBinding, error) {
	var review stockReadAuthReview
	var attempt bootstrapAttempt
	var record bootstrapRecord
	var user credential
	fail := func() (stockReadAuthBinding, error) { return stockReadAuthBinding{}, ErrBoundary }
	if !digest.MatchString(garmID) || !stockBindingJSON(raw[0], &review) || review.Version != "stock-read-auth-binding/v1" || review.GarmID != garmID || !digest.MatchString(review.LeafSHA256) || !digest.MatchString(review.AttemptSHA256) || !digest.MatchString(review.RecordSHA256) {
		return fail()
	}
	cert, err := x509.ParseCertificate(raw[1])
	if err != nil || cert.IsCA || cert.VerifyHostname("127.0.0.1") != nil || stockBindingDigest(raw[1]) != review.LeafSHA256 {
		return fail()
	}
	serverAuth := false
	for _, usage := range cert.ExtKeyUsage {
		if usage == x509.ExtKeyUsageServerAuth {
			serverAuth = true
		}
	}
	if !serverAuth || stockBindingDigest(raw[2]) != review.AttemptSHA256 || stockBindingDigest(raw[3]) != review.RecordSHA256 || !stockBindingJSON(raw[2], &attempt) || !stockBindingJSON(raw[3], &record) || !stockBindingJSON(raw[4], &user) || !validBootstrapCredential(user) {
		return fail()
	}
	wanted := bootstrapAttempt{"managed-garm-bootstrap/v1", garmID, credentialHash(user)}
	if attempt != wanted || record.bootstrapAttempt != wanted || !stockUUID(record.ControllerID) || record.UserID != "" {
		return fail()
	}
	return stockReadAuthBinding{review, record}, nil
}
func stockReadAuthFile(root *os.Root, name string) ([]byte, error) {
	// Names are selected only by the fixed reader below, never an external API.
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() {
		return nil, ErrBoundary
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrBoundary
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, ErrBoundary
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || !os.SameFile(before, fi) || !fi.Mode().IsRegular() || st.Uid != 0 || st.Nlink != 1 || fi.Mode().Perm() != 0600 || fi.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || fi.Size() <= 0 || fi.Size() > 16384 {
		return nil, ErrBoundary
	}
	raw, err := io.ReadAll(io.LimitReader(f, 16385))
	after, ae := f.Stat()
	last, le := root.Lstat(name)
	if err != nil || len(raw) != int(fi.Size()) || ae != nil || le != nil || !os.SameFile(fi, after) || !os.SameFile(fi, last) || after.Size() != fi.Size() || !after.ModTime().Equal(fi.ModTime()) {
		clear(raw)
		return nil, ErrBoundary
	}
	ast, ok := after.Sys().(*syscall.Stat_t)
	if !ok || ast.Ctim != st.Ctim {
		clear(raw)
		return nil, ErrBoundary
	}
	return raw, nil
}

type stockReadAuthMaterial struct {
	binding stockReadAuthBinding
	user    credential
	der     []byte
}

func (m *stockReadAuthMaterial) clear() { clear(m.der); m.der = nil; m.user = credential{} }
func loadStockReadAuthBinding(ctx context.Context, p stockReadProfile) (stockReadAuthBinding, error) {
	material, err := loadStockReadAuthMaterial(ctx, p)
	defer material.clear()
	return material.binding, err
}
func loadStockReadAuthMaterial(ctx context.Context, p stockReadProfile) (stockReadAuthMaterial, error) {
	fail := func() (stockReadAuthMaterial, error) { return stockReadAuthMaterial{}, ErrBoundary }
	if ctx == nil || ctx.Err() != nil || os.Geteuid() != 0 || !validStockReadProfile(p) {
		return fail()
	}
	path := filepath.Join(p.Directory, "supervisor-admin")
	if rootControlledAncestors(path) != nil {
		return fail()
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0700 {
		return fail()
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 {
		return fail()
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return fail()
	}
	defer root.Close()
	current, err := root.Stat(".")
	if err != nil || !os.SameFile(fi, current) {
		return fail()
	}
	names := [5]string{"stock-auth-binding.json", "stock-api.der", "bootstrap-attempt.json", "bootstrap-record.json", "bootstrap-user.json"}
	var raw [5][]byte
	defer func() {
		for _, b := range raw {
			clear(b)
		}
	}()
	for i, name := range names {
		raw[i], err = stockReadAuthFile(root, name)
		if err != nil || ctx.Err() != nil {
			return fail()
		}
	}
	binding, err := validateStockReadAuthBinding(p.IDs[0], raw)
	if err != nil {
		return fail()
	}
	// Bracket the entire file set, not merely each individual read.
	for i, name := range names {
		again, e := stockReadAuthFile(root, name)
		same := e == nil && bytes.Equal(raw[i], again)
		clear(again)
		if !same || ctx.Err() != nil {
			return fail()
		}
	}
	last, err := os.Lstat(path)
	if err != nil || !os.SameFile(fi, last) || !last.IsDir() || last.Mode().Perm() != 0700 || rootControlledAncestors(path) != nil || ctx.Err() != nil {
		return fail()
	}
	lastStat, ok := last.Sys().(*syscall.Stat_t)
	if !ok || lastStat.Uid != 0 || lastStat.Ctim != st.Ctim {
		return fail()
	}
	var user credential
	if !stockBindingJSON(raw[4], &user) {
		return fail()
	}
	return stockReadAuthMaterial{binding: binding, user: user, der: append([]byte(nil), raw[1]...)}, nil
}
