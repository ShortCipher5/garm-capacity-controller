//go:build linux

package nativebundle

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeegress"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeplanning"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativezfs"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
)

const RegistrationVersion = "native-template-registration/v1"

var ErrRegistration = errors.New("native template registration unavailable or mismatched; preserve consumed identity and partial output")
var registrationUser = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
var registrationFingerprint = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
var registrationDatasetPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var registrationLabel = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// These are deployment-owned inputs, not browser payloads. References name the
// exact frozen sections of one template, not arbitrary JSON merge instructions.
type RegistrationTemplate struct {
	Configuration                                         Configuration
	ArtifactReference, StorageReference, NetworkReference string
}
type RegistrationIdentity struct {
	Directory, Template, Administrator, HostPrefix string
}
type RegistrationAdministrator struct {
	Networks            []string
	Users               []string
	Ports               []uint16
	AdminKeyFingerprint string
}
type RegistrationCatalog struct {
	Templates                                  map[string]RegistrationTemplate
	Identities                                 map[string]RegistrationIdentity
	Administrators                             map[string]RegistrationAdministrator
	PrivateParent, OutputParent, JournalParent string
}
type RegistrationRequest struct {
	Revision, Template, Identity, Administrator           string
	ArtifactReference, StorageReference, NetworkReference string
	PreparationID, Host                                   string
	NativeTarget                                          nativeprovision.Target
	// Only the registration backend supplies its currently saved actual target.
	InventoryTarget sshinventory.Target
}
type RegistrationMetadata struct {
	Version, Revision, RegistrationDigest, TargetID, Template, Identity, Administrator string
	ArtifactReference, StorageReference, NetworkReference                              string
	ArtifactDigest, StorageDigest, NetworkDigest                                       string
	OwnerServerSPKI, OwnerClientSPKI                                                   string
	Binding                                                                            nativeplanning.Binding
	Build                                                                              Configuration
	OutputParent, JournalDirectory                                                     string
	ActivationAuthorized                                                               bool
}
type frozenTemplate struct {
	spec RegistrationTemplate
	raw  []byte
	sums map[string]string
}
type frozenIdentity struct {
	spec           RegistrationIdentity
	files          map[string][]byte
	server, client string
}
type RegistrationCompiler struct {
	templates                                            map[string]frozenTemplate
	identities                                           map[string]frozenIdentity
	administrators                                       map[string]RegistrationAdministrator
	privateParent, outputParent, journalParent, revision string
}

// Registration is configuration only. Materialize must be preceded by the
// caller's durable global reservation of ID, Identity and BOTH SPKI fingerprints.
// A copied Registration shares its local one-use bit. No restart permission is
// conferred; the durable registration journal must refuse any second attempt.
type Registration struct {
	metadata RegistrationMetadata
	files    map[string][]byte
	used     *atomic.Bool
}

func (r Registration) Metadata() RegistrationMetadata { return r.metadata }
func (c *RegistrationCompiler) Revision() string {
	if c == nil {
		return ""
	}
	return c.revision
}

func registrationDirectory(p string) bool {
	if protectedDirectory(p) != nil {
		return false
	}
	s, e := os.Lstat(p)
	return e == nil && s.Mode() == os.ModeDir|0700
}
func readRegistrationFile(dir, name string, limit int64) ([]byte, error) {
	if !registrationDirectory(dir) {
		return nil, ErrRegistration
	}
	r, e := os.OpenRoot(dir)
	if e != nil {
		return nil, ErrRegistration
	}
	defer r.Close()
	return read(r, name, limit, true)
}
func section(v map[string]any, k string, out any) error {
	raw, e := json.Marshal(get(v, k))
	if e != nil {
		return ErrRegistration
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return ErrRegistration
	}
	return nil
}
func registrationDataset(s string) bool {
	if len(s) > 240 || !strings.Contains(s, "/") {
		return false
	}
	for _, p := range strings.Split(s, "/") {
		if !registrationDatasetPart.MatchString(p) {
			return false
		}
	}
	return true
}
func registrationSections(v map[string]any) error {
	var a nativezfs.ArtifactConfiguration
	var s nativezfs.Configuration
	var b nativezfs.CategoryBudgets
	var n nativeegress.Specification
	if section(v, "artifacts", &a) != nil || section(v, "storage", &s) != nil || section(v, "storage_budgets", &b) != nil || section(v, "network", &n) != nil {
		return ErrRegistration
	}
	guid, e := strconv.ParseUint(s.RootGUID, 10, 64)
	if nativezfs.ValidateArtifactConfiguration(a) != nil || nativezfs.ValidateCategoryBudgets(s.QuotaBytes, b) != nil || e != nil || guid == 0 || strconv.FormatUint(guid, 10) != s.RootGUID || s.Registry != "" || !registrationDataset(s.RootDataset) || s.Binary != a.Binary || s.BinarySHA256 != a.BinarySHA256 || s.Helper != a.Helper || s.HelperSHA256 != a.HelperSHA256 {
		return ErrRegistration
	}
	if _, e := nativeegress.Compile(n); e != nil {
		return ErrRegistration
	}
	return nil
}

// NewRegistrationCompiler freezes private bytes and validates explicit profile
// shape. It does NOT execute artifacts or replace the actual owner checker.
func NewRegistrationCompiler(c RegistrationCatalog) (*RegistrationCompiler, error) {
	if len(c.Templates) < 1 || len(c.Templates) > 64 || len(c.Identities) < 1 || len(c.Identities) > 256 || len(c.Administrators) < 1 || len(c.Administrators) > 64 || !registrationDirectory(c.PrivateParent) || !registrationDirectory(c.OutputParent) || !registrationDirectory(c.JournalParent) || c.PrivateParent == c.OutputParent || c.PrivateParent == c.JournalParent || c.OutputParent == c.JournalParent {
		return nil, ErrRegistration
	}
	out := &RegistrationCompiler{templates: map[string]frozenTemplate{}, identities: map[string]frozenIdentity{}, administrators: map[string]RegistrationAdministrator{}, privateParent: c.PrivateParent, outputParent: c.OutputParent, journalParent: c.JournalParent}
	commitments := map[string]any{"version": RegistrationVersion, "catalog": c}
	totalBytes := 0
	for alias, a := range c.Administrators {
		if !registrationLabel.MatchString(alias) || len(a.Networks) < 1 || len(a.Networks) > 64 || len(a.Users) < 1 || len(a.Users) > 16 || len(a.Ports) < 1 || len(a.Ports) > 16 || !registrationFingerprint.MatchString(a.AdminKeyFingerprint) {
			return nil, ErrRegistration
		}
		for _, n := range a.Networks {
			p, e := netip.ParsePrefix(n)
			if e != nil || p.Masked() != p || p.String() != n || !p.Addr().IsGlobalUnicast() {
				return nil, ErrRegistration
			}
		}
		for _, u := range a.Users {
			if !registrationUser.MatchString(u) {
				return nil, ErrRegistration
			}
		}
		for _, p := range a.Ports {
			if p == 0 {
				return nil, ErrRegistration
			}
		}
		a.Networks = append([]string(nil), a.Networks...)
		a.Users = append([]string(nil), a.Users...)
		a.Ports = append([]uint16(nil), a.Ports...)
		out.administrators[alias] = a
	}
	for alias, t := range c.Templates {
		b := t.Configuration
		if !registrationLabel.MatchString(alias) || !registrationLabel.MatchString(t.ArtifactReference) || !registrationLabel.MatchString(t.StorageReference) || !registrationLabel.MatchString(t.NetworkReference) || b.ID != "" || b.OutputDirectory != "" || b.SelectionFile != "" || b.ExpectedBindingFile != "" || b.ExpectedSelectionDigest != "" || !hash(b.ExportManifestSHA256) || (b.Architecture != "amd64" && b.Architecture != "arm64") || protectedDirectory(b.ExportDirectory) != nil {
			return nil, ErrRegistration
		}
		raw, e := readRegistrationFile(b.PrivateDirectory, "capsule.json", 1<<20)
		if e != nil {
			return nil, ErrRegistration
		}
		totalBytes += len(raw)
		if totalBytes > 32<<20 {
			return nil, ErrRegistration
		}
		v, e := decodeObject(raw)
		if e != nil || registrationSections(v) != nil {
			return nil, ErrRegistration
		}
		control, e := object(v, "control")
		if e != nil {
			return nil, ErrRegistration
		}
		policy, e := object(control, "policy")
		if e != nil || (get(policy, "host") != nil && get(policy, "host") != "") {
			return nil, ErrRegistration
		}
		root, e := os.OpenRoot(b.ExportDirectory)
		if e != nil {
			return nil, ErrRegistration
		}
		sumBytes, e := read(root, "SHA256SUMS", 16<<10, false)
		root.Close()
		if e != nil || digest(sumBytes) != b.ExportManifestSHA256 {
			return nil, ErrRegistration
		}
		sums, e := checksumManifest(sumBytes)
		if e != nil {
			return nil, ErrRegistration
		}
		out.templates[alias] = frozenTemplate{t, raw, sums}
		commitments["template:"+alias] = digest(raw)
	}
	seen := map[string]bool{}
	for alias, i := range c.Identities {
		t, ok := out.templates[i.Template]
		_, admin := out.administrators[i.Administrator]
		if !registrationLabel.MatchString(alias) || !ok || !admin || (i.HostPrefix != "" && !registrationLabel.MatchString(i.HostPrefix)) {
			return nil, ErrRegistration
		}
		files := map[string][]byte{}
		for _, n := range []string{"owner-server.crt", "owner-server.key", "owner-client.crt", "owner-client.key", "controller-ca.crt"} {
			raw, e := readRegistrationFile(i.Directory, n, 1<<20)
			if e != nil {
				return nil, ErrRegistration
			}
			totalBytes += len(raw)
			if totalBytes > 32<<20 {
				return nil, ErrRegistration
			}
			files[n] = raw
			commitments[alias+":"+n] = digest(raw)
		}
		pins := []string{}
		for _, role := range []string{"owner-server", "owner-client"} {
			p, e := tls.X509KeyPair(files[role+".crt"], files[role+".key"])
			if e != nil {
				return nil, ErrRegistration
			}
			cert, e := x509.ParseCertificate(p.Certificate[0])
			if e != nil {
				return nil, ErrRegistration
			}
			pin := digest(cert.RawSubjectPublicKeyInfo)
			if seen[pin] {
				return nil, ErrRegistration
			}
			seen[pin] = true
			pins = append(pins, pin)
		}
		v, _ := decodeObject(t.raw)
		if rebind(v, strings.Repeat("0", 64), t.sums, files) != nil {
			return nil, ErrRegistration
		}
		out.identities[alias] = frozenIdentity{i, files, pins[0], pins[1]}
	}
	out.revision = nativeplanning.Digest(commitments)
	return out, nil
}
func (c *RegistrationCompiler) Compile(r RegistrationRequest) (Registration, error) {
	if c == nil || r.Revision != c.revision {
		return Registration{}, ErrRegistration
	}
	t, ok := c.templates[r.Template]
	i, identity := c.identities[r.Identity]
	a, admin := c.administrators[r.Administrator]
	if !ok || !identity || !admin || i.spec.Template != r.Template || i.spec.Administrator != r.Administrator || !strings.HasPrefix(r.Host, i.spec.HostPrefix) || r.ArtifactReference != t.spec.ArtifactReference || r.StorageReference != t.spec.StorageReference || r.NetworkReference != t.spec.NetworkReference {
		return Registration{}, ErrRegistration
	}
	binding := nativeplanning.Binding{PreparationID: r.PreparationID, Host: r.Host, NativeTarget: r.NativeTarget, InventoryTarget: r.InventoryTarget}
	if nativeplanning.ValidateBinding(binding) != nil || r.NativeTarget.AdminKeyFingerprint != a.AdminKeyFingerprint {
		return Registration{}, ErrRegistration
	}
	ip, _ := netip.ParseAddr(r.NativeTarget.Address)
	network, user, port := false, false, false
	for _, n := range a.Networks {
		p, _ := netip.ParsePrefix(n)
		network = network || p.Contains(ip)
	}
	for _, u := range a.Users {
		user = user || u == r.NativeTarget.AdminUser
	}
	for _, p := range a.Ports {
		port = port || p == r.NativeTarget.Port
	}
	if !network || !user || !port {
		return Registration{}, ErrRegistration
	}
	v, _ := decodeObject(t.raw)
	control, _ := object(v, "control")
	policy, _ := object(control, "policy")
	set(policy, "host", r.Host)
	if rebind(v, r.PreparationID, t.sums, i.files) != nil {
		return Registration{}, ErrRegistration
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return Registration{}, ErrRegistration
	}
	build := t.spec.Configuration
	build.ID = r.PreparationID
	build.PrivateDirectory = filepath.Join(c.privateParent, r.PreparationID)
	build.ExpectedBindingFile = filepath.Join(build.PrivateDirectory, "binding.json")
	m := RegistrationMetadata{Version: RegistrationVersion, Revision: c.revision, TargetID: r.InventoryTarget.ID, Template: r.Template, Identity: r.Identity, Administrator: r.Administrator, ArtifactReference: r.ArtifactReference, StorageReference: r.StorageReference, NetworkReference: r.NetworkReference, ArtifactDigest: nativeplanning.Digest(get(v, "artifacts")), StorageDigest: nativeplanning.Digest(struct{ Storage, Budgets any }{get(v, "storage"), get(v, "storage_budgets")}), NetworkDigest: nativeplanning.Digest(get(v, "network")), OwnerServerSPKI: i.server, OwnerClientSPKI: i.client, Binding: binding, Build: build, OutputParent: c.outputParent, JournalDirectory: filepath.Join(c.journalParent, r.PreparationID)}
	m.RegistrationDigest = nativeplanning.Digest(m)
	files := map[string][]byte{"capsule.json": raw}
	files["binding.json"], _ = json.Marshal(binding)
	for n, b := range i.files {
		files[n] = append([]byte(nil), b...)
	}
	return Registration{m, files, &atomic.Bool{}}, nil
}

// Materialize writes only fresh private configuration. Call only after durable
// identity/registration consumption. Any error is unknown: no cleanup or replay.
func (r Registration) Materialize(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil || r.used == nil || !r.used.CompareAndSwap(false, true) || r.metadata.RegistrationDigest == "" {
		return ErrRegistration
	}
	p := r.metadata.Build.PrivateDirectory
	if !registrationDirectory(filepath.Dir(p)) || os.Mkdir(p, 0700) != nil {
		return ErrRegistration
	}
	if os.Chmod(p, 0700) != nil || syncDirectory(filepath.Dir(p)) != nil {
		return ErrRegistration
	}
	root, e := os.OpenRoot(p)
	if e != nil {
		return ErrRegistration
	}
	defer root.Close()
	for n, b := range r.files {
		if ctx.Err() != nil || write(root, n, b) != nil {
			return ErrRegistration
		}
	}
	if syncDirectory(p) != nil {
		return ErrRegistration
	}
	return nil
}

// InspectMaterialized verifies an already completed private input after restart.
// It does not repair partial output, consume identity, or grant permission to
// prepare a host. The registration journal must separately prove completion.
func (r Registration) InspectMaterialized(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil || r.used == nil || r.metadata.RegistrationDigest == "" || !registrationDirectory(r.metadata.Build.PrivateDirectory) {
		return ErrRegistration
	}
	root, e := os.OpenRoot(r.metadata.Build.PrivateDirectory)
	if e != nil {
		return ErrRegistration
	}
	defer root.Close()
	entries, e := os.ReadDir(r.metadata.Build.PrivateDirectory)
	if e != nil || len(entries) != len(r.files) {
		return ErrRegistration
	}
	for n, expected := range r.files {
		actual, e := read(root, n, 1<<20, true)
		if e != nil || !bytes.Equal(actual, expected) || ctx.Err() != nil {
			clear(actual)
			return ErrRegistration
		}
		clear(actual)
	}
	return nil
}

// RegistrationCatalogView is a sanitized selection menu, not an authority or
// availability claim. The durable registry separately marks consumed identities.
type RegistrationCatalogView struct {
	Revision       string                     `json:"revision"`
	Templates      []RegistrationTemplateView `json:"templates"`
	Identities     []RegistrationIdentityView `json:"identities"`
	Administrators []string                   `json:"administrators"`
}
type RegistrationTemplateView struct {
	Name              string `json:"name"`
	ArtifactReference string `json:"artifact_reference"`
	StorageReference  string `json:"storage_reference"`
	NetworkReference  string `json:"network_reference"`
}
type RegistrationIdentityView struct {
	Name          string `json:"name"`
	Template      string `json:"template"`
	Administrator string `json:"administrator"`
	HostPrefix    string `json:"host_prefix"`
}

func (c *RegistrationCompiler) CatalogView() RegistrationCatalogView {
	v := RegistrationCatalogView{Templates: []RegistrationTemplateView{}, Identities: []RegistrationIdentityView{}, Administrators: []string{}}
	if c == nil {
		return v
	}
	v.Revision = c.revision
	for name, t := range c.templates {
		v.Templates = append(v.Templates, RegistrationTemplateView{name, t.spec.ArtifactReference, t.spec.StorageReference, t.spec.NetworkReference})
	}
	for name, i := range c.identities {
		v.Identities = append(v.Identities, RegistrationIdentityView{name, i.spec.Template, i.spec.Administrator, i.spec.HostPrefix})
	}
	for name := range c.administrators {
		v.Administrators = append(v.Administrators, name)
	}
	sort.Slice(v.Templates, func(i, j int) bool { return v.Templates[i].Name < v.Templates[j].Name })
	sort.Slice(v.Identities, func(i, j int) bool { return v.Identities[i].Name < v.Identities[j].Name })
	sort.Strings(v.Administrators)
	return v
}
