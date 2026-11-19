//go:build linux && stock_read_pilot_harness

package managedgarm

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// Expected disposable layout only. This helper does not inspect ownership,
// create paths, install files, or authorize any runtime operation.
type stockPilotFixtureDirectory struct {
	Path string
	UID  uint32
	Mode uint32
}

func stockPilotFixtureLayout() []stockPilotFixtureDirectory {
	root := stockPilotHarnessDirectory
	return []stockPilotFixtureDirectory{
		{stockReadProfileDirectory, 0, 0700}, {root, 0, 0700},
		{root + "/fixture", 0, 0700}, {root + "/fixture/private", 65530, 0700}, {root + "/fixture/state", 65530, 0700},
		{root + "/joined", 0, 0700}, {root + "/joined/gateway-private", 65532, 0700}, {root + "/joined/state", 65532, 0700}, {root + "/joined/ingress-private", 65531, 0700},
		{root + "/supervisor-admin", 0, 0700},
	}
}

// Bytes are private fixture material, never a public report. The caller must
// independently verify actual container/image/executable identities first.
// This is a renderer of those exact inputs, not an observation constructor.
type stockPilotFixtureFile struct {
	Path  string
	UID   uint32
	Mode  uint32
	Bytes []byte
}

func stockPilotFixtureFileAt(name string, raw []byte) stockPilotFixtureFile {
	return stockPilotFixtureFile{stockPilotHarnessDirectory + "/supervisor-admin/" + name, 0, 0600, append([]byte(nil), raw...)}
}
func stockPilotFixtureProvisional(p stockReadProfile, stockDER, userRaw []byte) ([]stockPilotFixtureFile, error) {
	if !validStockReadProfile(p) || p.Directory != stockPilotHarnessDirectory {
		return nil, ErrBoundary
	}
	var user credential
	if len(stockDER) > 16384 || len(userRaw) > 16384 || !stockBindingJSON(userRaw, &user) || !validBootstrapCredential(user) {
		return nil, ErrBoundary
	}
	review := stockPilotBootstrapReview{"stock-pilot-bootstrap/v1", p.IDs[0], stockBindingDigest(stockDER), credentialHash(user)}
	reviewed, err := json.Marshal(review)
	if err != nil {
		return nil, ErrBoundary
	}
	if _, _, err := validateStockPilotBootstrap(p.IDs[0], [3][]byte{reviewed, stockDER, userRaw}); err != nil {
		return nil, ErrBoundary
	}
	profile, err := json.Marshal(p)
	if err != nil {
		return nil, ErrBoundary
	}
	return []stockPilotFixtureFile{
		{stockReadProfileDirectory + "/reviewed-profile.json", 0, 0600, profile},
		stockPilotFixtureFileAt("stock-bootstrap-binding.json", reviewed),
		stockPilotFixtureFileAt("stock-api.der", stockDER),
		stockPilotFixtureFileAt("bootstrap-user.json", userRaw),
	}, nil
}
func stockPilotFixtureCompleted(p stockReadProfile, stockDER, userRaw, attemptRaw, recordRaw []byte) (stockPilotFixtureFile, error) {
	provisional, err := stockPilotFixtureProvisional(p, stockDER, userRaw)
	if err != nil {
		return stockPilotFixtureFile{}, err
	}
	defer func() {
		for _, file := range provisional {
			clear(file.Bytes)
		}
	}()
	if len(attemptRaw) > 16384 || len(recordRaw) > 16384 {
		return stockPilotFixtureFile{}, ErrBoundary
	}
	review := stockReadAuthReview{"stock-read-auth-binding/v1", p.IDs[0], stockBindingDigest(stockDER), stockBindingDigest(attemptRaw), stockBindingDigest(recordRaw)}
	reviewed, err := json.Marshal(review)
	if err != nil {
		return stockPilotFixtureFile{}, ErrBoundary
	}
	if _, err := validateStockReadAuthBinding(p.IDs[0], [5][]byte{reviewed, stockDER, attemptRaw, recordRaw, userRaw}); err != nil {
		return stockPilotFixtureFile{}, ErrBoundary
	}
	return stockPilotFixtureFileAt("stock-auth-binding.json", reviewed), nil
}

func TestStockPilotFixtureDocumentsUseExactExistingContracts(t *testing.T) {
	p, _ := stockReadFixture()
	p.Directory = stockPilotHarnessDirectory
	_, raw := stockAuthFixture(t)
	files, err := stockPilotFixtureProvisional(p, raw[1], raw[4])
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatal("unexpected initial file set")
	}
	wanted := []string{stockReadProfileDirectory + "/reviewed-profile.json", stockPilotHarnessDirectory + "/supervisor-admin/stock-bootstrap-binding.json", stockPilotHarnessDirectory + "/supervisor-admin/stock-api.der", stockPilotHarnessDirectory + "/supervisor-admin/bootstrap-user.json"}
	for i, f := range files {
		if f.Path != wanted[i] || f.UID != 0 || f.Mode != 0600 {
			t.Fatal("unexpected path/role")
		}
	}
	var profile stockReadProfile
	if !stockBindingJSON(files[0].Bytes, &profile) || profile != p {
		t.Fatal("profile changed")
	}
	if _, _, err := validateStockPilotBootstrap(p.IDs[0], [3][]byte{files[1].Bytes, files[2].Bytes, files[3].Bytes}); err != nil {
		t.Fatal(err)
	}
	completed, err := stockPilotFixtureCompleted(p, raw[1], raw[4], raw[2], raw[3])
	if err != nil {
		t.Fatal(err)
	}
	if completed.Path != stockPilotHarnessDirectory+"/supervisor-admin/stock-auth-binding.json" || completed.UID != 0 || completed.Mode != 0600 {
		t.Fatal("completion destination")
	}
	if _, err := validateStockReadAuthBinding(p.IDs[0], [5][]byte{completed.Bytes, raw[1], raw[2], raw[3], raw[4]}); err != nil {
		t.Fatal(err)
	}
	raw[1][0] ^= 1
	raw[4][0] ^= 1
	if _, _, err := validateStockPilotBootstrap(p.IDs[0], [3][]byte{files[1].Bytes, files[2].Bytes, files[3].Bytes}); err != nil {
		t.Fatal("output borrowed mutable inputs")
	}
}
func TestStockPilotFixtureRejectsUnboundAndFabricatedCompletion(t *testing.T) {
	p, _ := stockReadFixture()
	p.Directory = stockPilotHarnessDirectory
	_, raw := stockAuthFixture(t)
	for _, change := range []func(*stockReadProfile){
		func(p *stockReadProfile) { p.Directory = "/tmp/pilot" },
		func(p *stockReadProfile) { p.Directory += "/other" },
		func(p *stockReadProfile) { p.IDs[1] = p.IDs[0] },
		func(p *stockReadProfile) { p.Images[0] = "sha256:" + p.IDs[0] },
		func(p *stockReadProfile) { p.ExecutableSHA256[2] = "" },
	} {
		bad := p
		change(&bad)
		if files, err := stockPilotFixtureProvisional(bad, raw[1], raw[4]); err == nil || files != nil {
			t.Fatal("unbound profile rendered")
		}
	}
	for _, which := range []int{2, 3, 4} {
		bad := raw
		bad[which] = nil
		if _, err := stockPilotFixtureCompleted(p, bad[1], bad[4], bad[2], bad[3]); err == nil {
			t.Fatal("missing completed lineage rendered")
		}
	}
	var record bootstrapRecord
	if !stockBindingJSON(raw[3], &record) {
		t.Fatal("fixture")
	}
	record.UserID = "33333333-3333-4333-8333-333333333333"
	invented, _ := json.Marshal(record)
	if _, err := stockPilotFixtureCompleted(p, raw[1], raw[4], raw[2], invented); err == nil {
		t.Fatal("invented user identity rendered")
	}
	// Expected role-owned leaves have only root-owned expected ancestors.
	layout := stockPilotFixtureLayout()
	seen := map[string]uint32{}
	for _, d := range layout {
		if d.Mode != 0700 || !filepath.IsAbs(d.Path) || filepath.Clean(d.Path) != d.Path {
			t.Fatal("bad layout")
		}
		if _, dup := seen[d.Path]; dup {
			t.Fatal("duplicate layout")
		}
		seen[d.Path] = d.UID
	}
	for _, d := range layout {
		if d.UID != 0 {
			if uid, present := seen[filepath.Dir(d.Path)]; !present || uid != 0 {
				t.Fatal("role-owned parent")
			}
		}
	}
	layout[0].Path = "changed"
	if stockPilotFixtureLayout()[0].Path != stockReadProfileDirectory {
		t.Fatal("layout alias")
	}
}
