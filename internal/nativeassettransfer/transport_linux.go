//go:build linux

package nativeassettransfer

import (
	"bytes"
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassets"
	"golang.org/x/crypto/ssh"
)

//go:embed upload.py
var script string

type Target = nativeprovision.Target
type Plan struct {
	Target       Target                `json:"target"`
	Manifest     nativeassets.Manifest `json:"manifest"`
	Files        []File                `json:"files"`
	ScriptSHA256 string                `json:"script_sha256"`
}

func (b *Bundle) Plan(t Target) (Plan, error) {
	if b == nil {
		return Plan{}, ErrInvalid
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return Plan{}, ErrInvalid
	}
	m := b.manifest
	m.Files = map[string]nativeassets.Entry{}
	for k, v := range b.manifest.Files {
		m.Files[k] = v
	}
	p := Plan{t, m, append([]File{}, b.files...), digest([]byte(script))}
	if validate(p) != nil {
		return Plan{}, ErrInvalid
	}
	return p, nil
}
func Digest(p Plan) string {
	v, e := json.Marshal(p)
	if e != nil {
		return ""
	}
	return digest(v)
}
func validate(p Plan) error {
	t := p.Target
	a, e := netip.ParseAddr(t.Address)
	if e != nil || !a.IsGlobalUnicast() || a.String() != t.Address || t.Port == 0 || len(t.AdminUser) < 1 || len(t.AdminUser) > 32 || len(t.AdminKeyFingerprint) < 10 {
		return ErrInvalid
	}
	for _, c := range t.AdminUser {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return ErrInvalid
		}
	}
	key, e := hostbootstrap.PublicKey(t.HostKey)
	if e != nil || key.Type() != ssh.KeyAlgoED25519 || nativeassets.Validate(p.Manifest) != nil || p.ScriptSHA256 != digest([]byte(script)) {
		return ErrInvalid
	}
	names := append([]string{"capacity-native-assets"}, nativeassets.Names()...)
	if len(p.Files) != len(names) {
		return ErrInvalid
	}
	if p.Files[0].Name != "capacity-native-assets" {
		return ErrInvalid
	}
	for i, f := range p.Files {
		if f.Name != names[i] || !validHash(f.SHA256) || f.Size < 1 {
			return ErrInvalid
		}
		if i == 0 {
			if f.Size > 64<<20 {
				return ErrInvalid
			}
		} else if p.Manifest.Files[f.Name] != (nativeassets.Entry{SHA256: f.SHA256, Size: f.Size}) {
			return ErrInvalid
		}
	}
	return nil
}
func command() string {
	return "/usr/bin/sudo -n /usr/bin/python3 -I -c '" + strings.ReplaceAll(script, "'", "'\"'\"'") + "'"
}

type boundedOutput struct {
	bytes.Buffer
	max int
}

func (b *boundedOutput) Write(v []byte) (int, error) {
	if len(v) > b.max-b.Len() {
		return 0, ErrUnknown
	}
	return b.Buffer.Write(v)
}

func (b *Bundle) execute(ctx context.Context, p Plan, review string, key ssh.Signer, inspect bool, dial func(context.Context, string, string) (net.Conn, error)) (nativeassets.Receipt, error) {
	fail := func() (nativeassets.Receipt, error) { return nativeassets.Receipt{}, ErrUnknown }
	if b == nil || ctx == nil || ctx.Err() != nil || validate(p) != nil || review != Digest(p) || key == nil || ssh.FingerprintSHA256(key.PublicKey()) != p.Target.AdminKeyFingerprint {
		return nativeassets.Receipt{}, ErrInvalid
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nativeassets.Receipt{}, ErrInvalid
	}
	actual := Plan{p.Target, b.manifest, b.files, digest([]byte(script))}
	if Digest(actual) != review {
		return nativeassets.Receipt{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	address := net.JoinHostPort(p.Target.Address, strconv.Itoa(int(p.Target.Port)))
	conn, e := dial(ctx, "tcp", address)
	if e != nil {
		return fail()
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return fail()
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	host, _ := hostbootstrap.PublicKey(p.Target.HostKey)
	config := &ssh.ClientConfig{User: p.Target.AdminUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyAlgorithms: []string{ssh.KeyAlgoED25519}, HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
		if subtle.ConstantTimeCompare(k.Marshal(), host.Marshal()) != 1 {
			return ErrInvalid
		}
		return nil
	}}
	cc, ch, rq, e := ssh.NewClientConn(conn, address, config)
	if e != nil {
		return fail()
	}
	client := ssh.NewClient(cc, ch, rq)
	defer client.Close()
	session, e := client.NewSession()
	if e != nil {
		return fail()
	}
	defer session.Close()
	mode := "prepare"
	if inspect {
		mode = "inspect"
	}
	header, _ := json.Marshal(struct {
		Mode           string                `json:"mode"`
		Manifest       nativeassets.Manifest `json:"manifest"`
		ManifestJSON   string                `json:"manifest_json"`
		ManifestDigest string                `json:"manifest_digest"`
		Files          []File                `json:"files"`
	}{mode, p.Manifest, canonicalManifest(p.Manifest), manifestDigest(p.Manifest), p.Files})
	readers := []io.Reader{bytes.NewReader(append(header, '\n'))}
	if !inspect {
		for _, f := range b.files {
			readers = append(readers, io.NewSectionReader(b.handles[f.Name], 0, f.Size))
		}
	}
	session.Stdin = io.MultiReader(readers...)
	out := &boundedOutput{max: 16384}
	session.Stdout = out
	session.Stderr = io.Discard
	if session.Run(command()) != nil {
		return fail()
	}
	var receipt nativeassets.Receipt
	d := json.NewDecoder(bytes.NewReader(out.Bytes()))
	d.DisallowUnknownFields()
	if d.Decode(&receipt) != nil || d.Decode(new(any)) != io.EOF || nativeassets.ValidateReceipt(p.Manifest, receipt) != nil {
		return fail()
	}
	return receipt, nil
}
