//go:build linux

package nativeregistration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativebundle"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeplanning"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/crypto/ssh"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func addCatalogIdentity(t *testing.T, c *nativebundle.RegistrationCatalog, r Request) {
	t.Helper()
	dir := protectedTestDirectory(t)
	sc, sk := cert(t, 20)
	cc, ck := cert(t, 21)
	for name, b := range map[string][]byte{"owner-server.crt": sc, "owner-server.key": sk, "owner-client.crt": cc, "owner-client.key": ck, "controller-ca.crt": sc} {
		if e := os.WriteFile(filepath.Join(dir, name), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	c.Identities["owner2"] = nativebundle.RegistrationIdentity{Directory: dir, Template: r.Template, Administrator: r.Administrator, HostPrefix: "host-"}
}
func dualInventory(t *testing.T, s *Service, r *Request) sshinventory.Target {
	t.Helper()
	pub, private, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewPublicKey(pub)
	signer, _ := ssh.NewSignerFromKey(private)
	cfg := sshinventory.Config{Address: "127.0.0.1", Port: 22, User: "inventory", HostKey: string(ssh.MarshalAuthorizedKey(key)), CredentialRef: "inventory"}
	inv, e := sshinventory.NewService(map[string]sshinventory.Config{"one": cfg, "two": cfg}, func(context.Context, string) (ssh.Signer, error) { t.Fatal("network credential"); return signer, nil }, func(context.Context, string, string, string, string, bool) error {
		t.Fatal("network audit")
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	s.inventory = inv
	targets := inv.Targets()
	r.InventoryTargetID = targets[0].ID
	r.InventoryTargetDigest = nativeplanning.Digest(targets[0])
	return targets[1]
}
func registerCatalog(t *testing.T, s *Service, r Request) Configured {
	t.Helper()
	v, e := s.Review(t.Context(), r)
	if e != nil {
		t.Fatal(e)
	}
	c, e := s.Register(t.Context(), r, v.RegistrationDigest)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func reopenCatalog(t *testing.T, s *Service, c nativebundle.RegistrationCatalog) *Service {
	t.Helper()
	compiler, e := nativebundle.NewRegistrationCompiler(c)
	if e != nil {
		t.Fatal(e)
	}
	next, e := NewService(compiler, s.inventory, s.directory)
	if e != nil {
		t.Fatal(e)
	}
	return next
}
func TestCatalogAppendPreservesExactHistoricalConfigurationAndReservations(t *testing.T) {
	s, r, c := registryFixture(t)
	second := dualInventory(t, s, &r)
	old := registerCatalog(t, s, r)
	before, e := os.ReadFile(filepath.Join(s.directory, r.PreparationID+".intent.json"))
	if e != nil {
		t.Fatal(e)
	}
	addCatalogIdentity(t, &c, r)
	next := reopenCatalog(t, s, c)
	restored, history, e := next.Restore(t.Context())
	if e != nil || len(restored) != 1 || len(history) != 1 || !reflect.DeepEqual(restored[0].Metadata(), old.Metadata()) {
		t.Fatal("historical metadata drift", e)
	}
	q := r
	q.Revision = next.compiler.Revision()
	q.Identity = "owner2"
	q.PreparationID = strings.Repeat("b", 64)
	q.Host = "host-second"
	q.InventoryTargetID = second.ID
	q.InventoryTargetDigest = nativeplanning.Digest(second)
	registerCatalog(t, next, q)
	again := reopenCatalog(t, next, c)
	restored, history, e = again.Restore(t.Context())
	if e != nil || len(restored) != 2 || len(history) != 2 {
		t.Fatal("two revisions", e)
	}
	after, _ := os.ReadFile(filepath.Join(s.directory, r.PreparationID+".intent.json"))
	if string(before) != string(after) {
		t.Fatal("old intent rewritten")
	}
	if _, _, e = s.Restore(t.Context()); e == nil {
		t.Fatal("stale smaller catalog accepted newer head")
	}
	// Even a new reviewed revision cannot reuse old target/host/identity/keys.
	q.PreparationID = strings.Repeat("c", 64)
	v, e := next.Review(t.Context(), q)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = next.Register(t.Context(), q, v.RegistrationDigest); e == nil {
		t.Fatal("reused consumed reservation")
	}
}
func TestCatalogLegacyMigrationRequiresOriginalConfiguration(t *testing.T) {
	s, r, c := registryFixture(t)
	old := registerCatalog(t, s, r)
	if e := os.Remove(filepath.Join(s.directory, catalogName(1))); e != nil {
		t.Fatal(e)
	} // authentic pre-snapshot journal shape
	if _, _, e := s.Restore(t.Context()); e != nil {
		t.Fatal("read-only legacy restore", e)
	}
	if _, e := os.Stat(filepath.Join(s.directory, catalogName(1))); !os.IsNotExist(e) {
		t.Fatal("read-only restore wrote snapshot")
	}
	addCatalogIdentity(t, &c, r)
	next := reopenCatalog(t, s, c)
	if _, _, e := next.Restore(t.Context()); e == nil {
		t.Fatal("addition-first guessed legacy catalog")
	}
	if _, e := os.Stat(filepath.Join(s.directory, catalogName(1))); !os.IsNotExist(e) {
		t.Fatal("failed legacy migration wrote snapshot")
	}
	if e := s.AnchorCatalog(t.Context()); e != nil {
		t.Fatal("explicit legacy checkpoint", e)
	}
	if out, _, e := s.Restore(t.Context()); e != nil || len(out) != 1 || !reflect.DeepEqual(out[0].Metadata(), old.Metadata()) {
		t.Fatal("exact baseline migration", e)
	}
	if out, _, e := next.Restore(t.Context()); e != nil || len(out) != 1 {
		t.Fatal("append after checkpoint", e)
	}
}
func TestCatalogAppendRetainsUnknownAndRejectsHistoryTampering(t *testing.T) {
	for _, mode := range []string{"unknown", "changed_alias", "changed_bytes", "missing_snapshot", "orphan_snapshot", "broken_chain"} {
		t.Run(mode, func(t *testing.T) {
			s, r, c := registryFixture(t)
			v, e := s.Review(t.Context(), r)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "unknown" {
				os.Mkdir(filepath.Join(c.PrivateParent, r.PreparationID), 0700)
				if _, e = s.Register(t.Context(), r, v.RegistrationDigest); e == nil {
					t.Fatal("must remain unknown")
				}
			} else {
				registerCatalog(t, s, r)
			}
			addCatalogIdentity(t, &c, r)
			next := reopenCatalog(t, s, c)
			switch mode {
			case "changed_alias":
				a := c.Administrators[r.Administrator]
				a.Ports = []uint16{22, 23}
				c.Administrators[r.Administrator] = a
				next = reopenCatalog(t, s, c)
			case "changed_bytes":
				p := filepath.Join(c.Templates[r.Template].Configuration.PrivateDirectory, "capsule.json")
				raw, _ := os.ReadFile(p)
				os.WriteFile(p, append(raw, ' '), 0600)
				next = reopenCatalog(t, s, c)
			case "missing_snapshot":
				os.Remove(filepath.Join(s.directory, catalogName(1)))
			case "orphan_snapshot":
				os.WriteFile(filepath.Join(s.directory, catalogName(3)), []byte("{}"), 0600)
			case "broken_chain":
				var rec catalogRecord
				s.read(catalogName(1), &rec)
				rec.Previous = strings.Repeat("d", 64)
				os.Remove(filepath.Join(s.directory, catalogName(1)))
				s.write(catalogName(1), rec)
			}
			out, h, e := next.Restore(t.Context())
			if mode == "unknown" {
				if e != nil || len(out) != 0 || len(h) != 1 || h[0].State != "registration_unknown" {
					t.Fatal(h, e)
				}
				q := r
				q.Revision = next.compiler.Revision()
				q.PreparationID = strings.Repeat("c", 64)
				q.Host = "host-other"
				review, e := next.Review(t.Context(), q)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = next.Register(t.Context(), q, review.RegistrationDigest); e == nil {
					t.Fatal("unknown reservation reused")
				}
			} else if e == nil {
				t.Fatal("corrupt or changed history accepted")
			}
		})
	}
}

func TestCatalogStartupAdditionCannotBeRemovedEvenBeforeUse(t *testing.T) {
	s, r, c := registryFixture(t)
	if e := s.AnchorCatalog(t.Context()); e != nil {
		t.Fatal(e)
	}
	addCatalogIdentity(t, &c, r)
	next := reopenCatalog(t, s, c)
	if e := next.AnchorCatalog(t.Context()); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(s.directory, catalogName(2))); e != nil {
		t.Fatal("new startup revision not anchored", e)
	}
	if _, _, e := s.Restore(t.Context()); e == nil {
		t.Fatal("removed unused startup-approved addition")
	}
	if e := s.AnchorCatalog(t.Context()); e == nil {
		t.Fatal("stale process anchored old catalog")
	}
	out, h, e := next.Restore(t.Context())
	if e != nil || len(out) != 0 || len(h) != 0 {
		t.Fatal("catalog anchoring fabricated registration", e)
	}
}
