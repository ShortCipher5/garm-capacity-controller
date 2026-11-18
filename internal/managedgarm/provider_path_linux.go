package managedgarm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/profilesnapshot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
)

// ProviderPathPins are independently approved executable digests, not evidence.
type ProviderPathPins struct{ GarmSHA256, BridgeSHA256 string }
type ProviderPathObservation struct {
	at          time.Time
	fingerprint string
}

func (o ProviderPathObservation) ObservedAt() time.Time { return o.at }

// ProviderPathObserver inspects a real managed process/filesystem, real scoped
// stock pools and the canonical ledger. It latches closed on any drift. This is
// NOT activation authority: file inspection cannot prove GARM's loaded in-memory
// configuration, exclusive native execution or safe physical retirement.
type ProviderPathObserver struct {
	mu       sync.Mutex
	session  *Session
	profiles *providerProfileClient
	pins     ProviderPathPins
	origin   *ProviderOriginConfiguration
	initial  string
	revoked  bool
}

func NewProviderPathObserver(ctx context.Context, s *Session, source ProviderProfileConfiguration, pins ProviderPathPins) (*ProviderPathObserver, error) {
	if os.Geteuid() != 0 || s == nil || source.Origin == nil || !digest.MatchString(pins.GarmSHA256) || !digest.MatchString(pins.BridgeSHA256) {
		return nil, ErrBoundary
	}
	profiles, err := newProviderProfileClient(s, source)
	if err != nil {
		return nil, err
	}
	origin := *source.Origin
	o := &ProviderPathObserver{session: s, profiles: profiles, pins: pins, origin: &origin}
	if _, err := o.Check(ctx); err != nil {
		return nil, err
	}
	return o, nil
}
func (o *ProviderPathObserver) Check(ctx context.Context) (ProviderPathObservation, error) {
	if o == nil || ctx == nil {
		return ProviderPathObservation{}, ErrBoundary
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	fail := func() (ProviderPathObservation, error) {
		o.revoked = true
		return ProviderPathObservation{}, ErrBoundary
	}
	if o.revoked || ctx.Err() != nil || os.Geteuid() != 0 {
		return fail()
	}
	if o.session.Verify(ctx) != nil {
		return fail()
	}
	topology, err := Observe(ctx, o.session.spec)
	if err != nil || topology.Namespace() != o.session.namespace {
		return fail()
	}
	state, err := o.profiles.read(ctx)
	if err != nil || len(state.Payload.Profiles) == 0 || len(state.Payload.Profiles) > 256 || len(state.Payload.Profiles) != len(state.Payload.PoolHosts) {
		return fail()
	}
	if validateProviderBindings(state.Payload) != nil {
		return fail()
	}
	actualOrigin, err := observeProviderOrigin(ctx, o.session, o.origin)
	if err != nil {
		return fail()
	}
	for _, profile := range state.Payload.Profiles {
		if profile.SourceIdentity != actualOrigin {
			return fail()
		}
	}
	controller := o.session.ControllerID()
	names, err := providerNames(state.Payload.Profiles, controller)
	if err != nil {
		return fail()
	}
	root, err := os.OpenRoot(fmt.Sprintf("/proc/%d/root", topology.garmPID))
	if err != nil {
		return fail()
	}
	defer root.Close()
	config, _, err := providerFile(root, "etc/garm/garm.toml", 1<<20, 65530, false)
	if err != nil || validateProviderTOML(config, names) != nil {
		return fail()
	}
	garmInfo, err := providerExecutable(root, "usr/bin/garm", o.pins.GarmSHA256)
	if err != nil {
		return fail()
	}
	_, err = providerExecutable(root, "usr/local/bin/capacity-provider", o.pins.BridgeSHA256)
	if err != nil {
		return fail()
	}
	actual, err := os.Stat(fmt.Sprintf("/proc/%d/exe", topology.garmPID))
	if err != nil || !os.SameFile(actual, garmInfo) {
		return fail()
	}
	command, err := boundedRead(fmt.Sprintf("/proc/%d/cmdline", topology.garmPID), 8192)
	if err != nil || !bytes.Equal(command, []byte("/usr/bin/garm\x00-config\x00/etc/garm/garm.toml\x00")) {
		return fail()
	}
	status, err := boundedRead(fmt.Sprintf("/proc/%d/status", topology.garmPID), 64<<10)
	if err != nil || !providerProcessIdentity(status) {
		return fail()
	}
	stockPublic, _, err := providerFile(root, "etc/garm/api-cert.pem", 16384, 65530, false)
	if err != nil {
		return fail()
	}
	block, rest := pem.Decode(stockPublic)
	if block == nil || block.Type != "CERTIFICATE" || strings.TrimSpace(string(rest)) != "" {
		return fail()
	}
	stockServer, err := x509.ParseCertificate(block.Bytes)
	if err != nil || hashBytes(stockServer.Raw) != o.session.spec.GarmServerFingerprint || o.profiles.clientKey == hashBytes(stockServer.RawSubjectPublicKeyInfo) {
		return fail()
	}
	evidence := []string{topology.namespace, fmt.Sprint(topology.garmPID), fmt.Sprint(topology.processStart), o.session.Identity(), hashBytes(config), o.pins.GarmSHA256, o.pins.BridgeSHA256}
	for _, name := range names {
		if ctx.Err() != nil {
			return fail()
		}
		p := profileForProvider(state.Payload.Profiles, name)
		raw, _, err := providerFile(root, "etc/garm/provider-"+name+".json", 16384, 65530, false)
		if err != nil {
			return fail()
		}
		cfg, err := decodeProviderConfiguration(raw, name, controller)
		if err != nil {
			return fail()
		}
		maintenance, maintenanceHash, err := providerTLS(root, cfg.TLS)
		if err != nil {
			return fail()
		}
		stock, stockHash, err := providerTLS(root, *cfg.StockTLS)
		if err != nil || hashBytes(stock.Certificate[0]) != p.Authority.ClientFingerprint || bytes.Equal(stock.Leaf.RawSubjectPublicKeyInfo, maintenance.Leaf.RawSubjectPublicKeyInfo) || o.profiles.clientKey == hashBytes(stock.Leaf.RawSubjectPublicKeyInfo) || o.profiles.clientKey == hashBytes(maintenance.Leaf.RawSubjectPublicKeyInfo) {
			return fail()
		}
		evidence = append(evidence, name, hashBytes(raw), maintenanceHash, stockHash, fmt.Sprint(p.Sequence), p.PoolFingerprint)
	}
	pools, err := o.session.ReadCollection(ctx, "/api/v1/pools")
	if err != nil || verifyProviderPools(pools, state.Payload.Profiles) != nil {
		return fail()
	}
	// Re-read the actual process and immutable profile sequence mapping. Changed
	// accounting by itself is normal; changed enrolled identities are not.
	after, err := o.profiles.read(ctx)
	if err != nil || !sameProviderProfiles(state.Payload.Profiles, after.Payload.Profiles) {
		return fail()
	}
	start, err := processStartTime(topology.garmPID)
	if err != nil || start != topology.processStart || o.session.Verify(ctx) != nil {
		return fail()
	}
	encoded, _ := json.Marshal(evidence)
	fp := hashBytes(encoded)
	if o.initial != "" && o.initial != fp {
		return fail()
	}
	o.initial = fp
	return ProviderPathObservation{at: time.Now().UTC(), fingerprint: fp}, nil
}
func hashBytes(v []byte) string { s := sha256.Sum256(v); return hex.EncodeToString(s[:]) }

var providerName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

func providerNames(profiles map[string]admission.BrokerPoolProfile, controller string) ([]string, error) {
	if !stockUUID(controller) {
		return nil, ErrBoundary
	}
	names := []string{}
	seen := map[string]bool{}
	for id, p := range profiles {
		n := p.Authority.ProviderName
		if p.Pool != id || p.Authority.PoolID != id || p.Authority.Controller != controller || p.Sequence <= 0 || !providerName.MatchString(n) || seen[n] {
			return nil, ErrBoundary
		}
		// Current stock provider credentials are pool-scoped, so one configured
		// external provider instance cannot claim several independent pool scopes.
		seen[n] = true
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}
func profileForProvider(p map[string]admission.BrokerPoolProfile, name string) admission.BrokerPoolProfile {
	for _, v := range p {
		if v.Authority.ProviderName == name {
			return v
		}
	}
	return admission.BrokerPoolProfile{}
}
func sameProviderProfiles(a, b map[string]admission.BrokerPoolProfile) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || v.Sequence != w.Sequence {
			return false
		}
	}
	return true
}
func providerFile(root *os.Root, path string, limit int64, uid uint32, executable bool) ([]byte, os.FileInfo, error) {
	if filepath.IsAbs(path) || filepath.Clean(path) != path || strings.HasPrefix(path, "../") {
		return nil, nil, ErrBoundary
	}
	pathInfo, pathErr := providerPathNoLinks(root, path)
	if pathErr != nil {
		return nil, nil, ErrBoundary
	}
	f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, ErrBoundary
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, ErrBoundary
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !os.SameFile(pathInfo, info) || !info.Mode().IsRegular() || st.Uid != uid || st.Nlink != 1 || info.Mode().Perm()&0022 != 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || info.Size() <= 0 || info.Size() > limit || (executable && info.Mode().Perm()&0111 == 0) || (!executable && info.Mode().Perm()&0077 != 0) {
		return nil, nil, ErrBoundary
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, nil, ErrBoundary
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, nil, ErrBoundary
	}
	afterStat, ok := after.Sys().(*syscall.Stat_t)
	if !ok || afterStat.Ctim != st.Ctim {
		return nil, nil, ErrBoundary
	}
	finalPath, pathErr := providerPathNoLinks(root, path)
	if pathErr != nil || !os.SameFile(info, finalPath) {
		return nil, nil, ErrBoundary
	}
	return raw, info, nil
}
func providerProcessIdentity(raw []byte) bool {
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok {
			values[k] = strings.Join(strings.Fields(v), " ")
		}
	}
	return values["Uid"] == "65530 65530 65530 65530" && values["Gid"] == "65530 65530 65530 65530" && values["CapEff"] == "0000000000000000" && values["NoNewPrivs"] == "1"
}

type providerConfiguration struct {
	Gateway      string                `json:"gateway"`
	Controller   string                `json:"controller"`
	TLS          securefiles.TLSFiles  `json:"tls"`
	StockTLS     *securefiles.TLSFiles `json:"stock_tls"`
	CreateOrigin string                `json:"create_origin"`
}

func decodeProviderConfiguration(raw []byte, name, controller string) (providerConfiguration, error) {
	var p providerConfiguration
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF || p.Gateway != "https://127.0.0.1:9443" || p.Controller != controller || p.CreateOrigin != "stock-provider-v1" || p.StockTLS == nil {
		return p, ErrBoundary
	}
	// Require canonical JSON as produced by the owned deployment, rejecting
	// duplicate/case-folded fields as well as unknown direct-native credentials.
	canonical, _ := json.Marshal(p)
	if !bytes.Equal(bytes.TrimSpace(raw), canonical) {
		return p, ErrBoundary
	}
	expected := func(role string) securefiles.TLSFiles {
		base := "/etc/garm/provider-" + name + "-" + role
		return securefiles.TLSFiles{CA: base + "-ca.pem", Certificate: base + "-cert.pem", Key: base + "-key.pem"}
	}
	if p.TLS != expected("maintenance") || *p.StockTLS != expected("stock") {
		return p, ErrBoundary
	}
	return p, nil
}
func providerTLS(root *os.Root, files securefiles.TLSFiles) (tls.Certificate, string, error) {
	ca, _, err := providerFile(root, strings.TrimPrefix(files.CA, "/"), 64<<10, 65530, false)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	cert, _, err := providerFile(root, strings.TrimPrefix(files.Certificate, "/"), 16<<10, 65530, false)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	key, _, err := providerFile(root, strings.TrimPrefix(files.Key, "/"), 16<<10, 65530, false)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil || len(pair.Certificate) != 1 {
		return tls.Certificate{}, "", ErrBoundary
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) {
		return tls.Certificate{}, "", ErrBoundary
	}
	clientUsage := len(leaf.ExtKeyUsage) == 0
	for _, u := range leaf.ExtKeyUsage {
		if u == x509.ExtKeyUsageClientAuth || u == x509.ExtKeyUsageAny {
			clientUsage = true
		}
	}
	if !clientUsage {
		return tls.Certificate{}, "", ErrBoundary
	}
	rest := ca
	count := 0
	for len(bytes.TrimSpace(rest)) > 0 {
		block, tail := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" {
			return tls.Certificate{}, "", ErrBoundary
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return tls.Certificate{}, "", ErrBoundary
		}
		rest = tail
		count++
	}
	if count == 0 {
		return tls.Certificate{}, "", ErrBoundary
	}
	pair.Leaf = leaf
	return pair, hashBytes(append(append(ca, cert...), key...)), nil
}
func verifyProviderPools(raw []byte, profiles map[string]admission.BrokerPoolProfile) error {
	if len(raw) > 4<<20 {
		return ErrBoundary
	}
	var rows []json.RawMessage
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '[' || json.Unmarshal(raw, &rows) != nil || len(rows) != len(profiles) {
		return ErrBoundary
	}
	seen := map[string]bool{}
	for _, row := range rows {
		facts, err := stockidentity.ParseFacts(row)
		if err != nil {
			return ErrBoundary
		}
		p, ok := profiles[facts.Pool]
		if !ok || seen[facts.Pool] || facts.Repository != p.Repository || facts.Provider != p.Authority.ProviderName || facts.Fingerprint() != p.PoolFingerprint {
			return ErrBoundary
		}
		seen[facts.Pool] = true
	}
	return nil
}

// Hash large executables through a fixed-size buffer instead of retaining their
// contents. Both file identity and metadata are rechecked before returning.
func providerExecutable(root *os.Root, path, expected string) (os.FileInfo, error) {
	pathInfo, pathErr := providerPathNoLinks(root, path)
	if pathErr != nil {
		return nil, ErrBoundary
	}

	f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrBoundary
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, ErrBoundary
	}
	st, ok := before.Sys().(*syscall.Stat_t)
	if !ok || !os.SameFile(pathInfo, before) || !before.Mode().IsRegular() || st.Uid != 0 || st.Nlink != 1 || before.Mode().Perm()&0022 != 0 || before.Mode().Perm()&0111 == 0 || before.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || before.Size() <= 0 || before.Size() > 128<<20 {
		return nil, ErrBoundary
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (128<<20)+1))
	if err != nil || n != before.Size() || hex.EncodeToString(h.Sum(nil)) != expected {
		return nil, ErrBoundary
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, ErrBoundary
	}
	ast, ok := after.Sys().(*syscall.Stat_t)
	if !ok || ast.Ctim != st.Ctim {
		return nil, ErrBoundary
	}
	finalPath, pathErr := providerPathNoLinks(root, path)
	if pathErr != nil || !os.SameFile(after, finalPath) {
		return nil, ErrBoundary
	}
	return after, nil
}

// EncodeManagedProviderCredentials emits the fixed, non-native credential
// references for the owned provider. It does not create or distribute any key.
func EncodeManagedProviderCredentials(name, controller string) ([]byte, error) {
	if !providerName.MatchString(name) || !stockUUID(controller) {
		return nil, ErrBoundary
	}
	paths := func(role string) securefiles.TLSFiles {
		base := "/etc/garm/provider-" + name + "-" + role
		return securefiles.TLSFiles{CA: base + "-ca.pem", Certificate: base + "-cert.pem", Key: base + "-key.pem"}
	}
	stock := paths("stock")
	return json.Marshal(providerConfiguration{Gateway: "https://127.0.0.1:9443", Controller: controller, TLS: paths("maintenance"), StockTLS: &stock, CreateOrigin: "stock-provider-v1"})
}

func providerPathNoLinks(root *os.Root, path string) (os.FileInfo, error) {
	if filepath.IsAbs(path) || filepath.Clean(path) != path || path == "." || strings.HasPrefix(path, "../") {
		return nil, ErrBoundary
	}
	parts := strings.Split(path, "/")
	prefix := ""
	var info os.FileInfo
	for i, p := range parts {
		if prefix != "" {
			prefix += "/"
		}
		prefix += p
		var err error
		info, err = root.Lstat(prefix)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !info.IsDir()) {
			return nil, ErrBoundary
		}
	}
	return info, nil
}

func validateProviderBindings(p profilesnapshot.Payload) error {
	if len(p.Profiles) != len(p.PoolHosts) {
		return ErrBoundary
	}
	for id, profile := range p.Profiles {
		hosts, ok := p.PoolHosts[id]
		if !ok || len(hosts) == 0 {
			return ErrBoundary
		}
		domains := map[string]int64{}
		for _, d := range profile.Domains {
			if _, ok := domains[d.Host]; ok {
				return ErrBoundary
			}
			domains[d.Host] = d.HostBindingSequence
		}
		seen := map[string]bool{}
		for _, host := range hosts {
			seq, ok := p.HostBindings[host]
			if !ok || seq <= 0 || seen[host] || domains[host] != seq {
				return ErrBoundary
			}
			seen[host] = true
		}
	}
	return nil
}
