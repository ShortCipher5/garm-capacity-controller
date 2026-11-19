package managedgarm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Bootstrap outcome files contain no password/JWT. The exclusive attempted
// marker is fsynced before first-run; lost responses never cause its replay.
type bootstrapAttempt struct{ Version, GarmID, CredentialDigest string }
type bootstrapRecord struct {
	bootstrapAttempt
	ControllerID, UserID string
}
type bootstrapFiles struct {
	root *os.Root
	uid  uint32
}

func openBootstrapFiles(path string, uid uint32) (*bootstrapFiles, error) {
	if uid == 0 && rootControlledAncestors(path) != nil {
		return nil, ErrBoundary
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrBoundary
	}
	real, e := filepath.EvalSymlinks(path)
	if e != nil || real != path {
		return nil, ErrBoundary
	}
	root, e := os.OpenRoot(path)
	if e != nil {
		return nil, ErrBoundary
	}
	f := &bootstrapFiles{root, uid}
	info, e := root.Stat(".")
	if e != nil || !f.private(info, true) {
		root.Close()
		return nil, ErrBoundary
	}
	return f, nil
}
func (f *bootstrapFiles) private(info os.FileInfo, dir bool) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == f.uid && info.Mode().Perm()&0077 == 0 && info.IsDir() == dir && (dir || info.Mode().IsRegular() && st.Nlink == 1)
}
func (f *bootstrapFiles) read(name string, out any) error {
	file, e := f.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return e
	}
	defer file.Close()
	info, e := file.Stat()
	if e != nil || !f.private(info, false) || info.Size() > 16384 {
		return ErrBoundary
	}
	raw, e := io.ReadAll(io.LimitReader(file, 16385))
	if e != nil || len(raw) > 16384 {
		return ErrBoundary
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return ErrBoundary
	}
	return nil
}
func (f *bootstrapFiles) create(name string, v any) error {
	raw, e := json.Marshal(v)
	if e != nil {
		return ErrBoundary
	}
	file, e := f.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	_, e = file.Write(raw)
	if e == nil {
		e = file.Sync()
	}
	ce := file.Close()
	if e != nil || ce != nil {
		return ErrBoundary
	}
	dir, e := f.root.Open(".")
	if e != nil {
		return ErrBoundary
	}
	defer dir.Close()
	return dir.Sync()
}
func credentialHash(c credential) string {
	raw, _ := json.Marshal(c)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func validBootstrapCredential(c credential) bool {
	if c.Username != "capacitycontroller" || len(c.Password) != 64 || !digest.MatchString(c.Password) || c.Email != "capacity-controller@localhost.invalid" || c.FullName != "Managed capacity controller" {
		return false
	}
	return true
}
func stockUUID(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' || s != strings.ToLower(s) {
		return false
	}
	v, e := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	return e == nil && len(v) == 16 && s != "00000000-0000-0000-0000-000000000000"
}

// bootstrapExchange is internal so test protocol fixtures cannot mint a real
// Session. Production supplies only the observed namespace-bound transport.
type bootstrapExchange func(context.Context, string, string, string, any) ([]byte, error)

func bootstrapProtocol(ctx context.Context, files *bootstrapFiles, garmID string, exchange bootstrapExchange) (bootstrapRecord, string, error) {
	var c credential
	if files.read("bootstrap-user.json", &c) != nil || !validBootstrapCredential(c) || !digest.MatchString(garmID) {
		return bootstrapRecord{}, "", ErrBoundary
	}
	wanted := bootstrapAttempt{"managed-garm-bootstrap/v1", garmID, credentialHash(c)}
	var attempted bootstrapAttempt
	e := files.read("bootstrap-attempt.json", &attempted)
	if errors.Is(e, os.ErrNotExist) {
		// A success without its durable attempt is a broken lineage, not fresh setup.
		var existing bootstrapRecord
		if e = files.read("bootstrap-record.json", &existing); !errors.Is(e, os.ErrNotExist) {
			return bootstrapRecord{}, "", ErrBoundary
		}
		if files.create("bootstrap-attempt.json", wanted) != nil {
			return bootstrapRecord{}, "", ErrBoundary
		}
		attempted = wanted
		// Even definite HTTP failure cannot authorize another first-run. Login below
		// is read-only authentication recovery against the same generated credential.
		raw, err := exchange(ctx, http.MethodPost, "/api/v1/first-run", "", c)
		if err == nil {
			var user struct {
				ID       string `json:"id"`
				Username string `json:"username"`
				Email    string `json:"email"`
				FullName string `json:"full_name"`
				Enabled  bool   `json:"enabled"`
				Admin    bool   `json:"is_admin"`
			}
			if json.Unmarshal(raw, &user) != nil || !stockUUID(user.ID) || user.Username != c.Username || user.Email != c.Email || !user.Enabled || !user.Admin {
				return bootstrapRecord{}, "", ErrBoundary
			}
		}
	} else if e != nil || attempted != wanted {
		return bootstrapRecord{}, "", ErrBoundary
	}
	record, token, e := authenticateBootstrapCredential(ctx, c, wanted, exchange)
	if e != nil {
		return bootstrapRecord{}, "", e
	}
	var previous bootstrapRecord
	if e = files.read("bootstrap-record.json", &previous); errors.Is(e, os.ErrNotExist) {
		if files.create("bootstrap-record.json", record) != nil {
			return bootstrapRecord{}, "", ErrBoundary
		}
	} else if e != nil || previous != record {
		return bootstrapRecord{}, "", ErrBoundary
	}
	return record, token, nil
}

// authenticateBootstrapCredential performs only the existing fixed login and
// controller read. Durable setup and record creation remain bootstrap-only.
func authenticateBootstrapCredential(ctx context.Context, c credential, wanted bootstrapAttempt, exchange bootstrapExchange) (bootstrapRecord, string, error) {
	raw, e := exchange(ctx, http.MethodPost, "/api/v1/auth/login", "", struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{c.Username, c.Password})
	if e != nil {
		return bootstrapRecord{}, "", ErrBoundary
	}
	var auth struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(raw, &auth) != nil || auth.Token == "" || len(auth.Token) > 16384 || strings.ContainsAny(auth.Token, "\r\n\x00") {
		return bootstrapRecord{}, "", ErrBoundary
	}
	raw, e = exchange(ctx, http.MethodGet, "/api/v1/controller", auth.Token, nil)
	if e != nil {
		return bootstrapRecord{}, "", ErrBoundary
	}
	var controller struct {
		ID string `json:"controller_id"`
	}
	if json.Unmarshal(raw, &controller) != nil || !stockUUID(controller.ID) {
		return bootstrapRecord{}, "", ErrBoundary
	}
	record := bootstrapRecord{bootstrapAttempt: wanted, ControllerID: controller.ID}
	// UserID is intentionally not inferred from unsigned JWT claims. Stock has no
	// authenticated current-user read; successful generated-password login and
	// fresh owned deployment are the custody assumptions, not external attestation.
	return record, auth.Token, nil
}

// authenticateExistingProtocol cannot initialize or repair bootstrap state. Its
// expected record was read by OpenSession before checking actual topology/TLS.
// The concrete Session exchange retains every existing fail-closed check.
func authenticateExistingProtocol(ctx context.Context, files *bootstrapFiles, garmID string, expected bootstrapRecord, exchange bootstrapExchange) (bootstrapRecord, string, error) {
	var c credential
	if files.read("bootstrap-user.json", &c) != nil || !validBootstrapCredential(c) || !digest.MatchString(garmID) {
		return bootstrapRecord{}, "", ErrBoundary
	}
	wanted := bootstrapAttempt{"managed-garm-bootstrap/v1", garmID, credentialHash(c)}
	var attempt bootstrapAttempt
	if files.read("bootstrap-attempt.json", &attempt) != nil || attempt != wanted || expected.bootstrapAttempt != wanted || !stockUUID(expected.ControllerID) {
		return bootstrapRecord{}, "", ErrBoundary
	}
	record, token, err := authenticateBootstrapCredential(ctx, c, wanted, exchange)
	if err != nil || record != expected {
		return bootstrapRecord{}, "", ErrBoundary
	}
	// Preserve the original post-authentication protected record reread, without
	// the bootstrap-only missing-record creation branch.
	var after bootstrapRecord
	if files.read("bootstrap-record.json", &after) != nil || after != record {
		return bootstrapRecord{}, "", ErrBoundary
	}
	return record, token, nil
}
