package managedgarm

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func stockAuthFixture(t *testing.T) (string, [5][]byte) {
	t.Helper()
	id := strings.Repeat("a", 64)
	user := credential{Username: "capacitycontroller", Password: strings.Repeat("b", 64), Email: "capacity-controller@localhost.invalid", FullName: "Managed capacity controller"}
	attempt := bootstrapAttempt{"managed-garm-bootstrap/v1", id, credentialHash(user)}
	calls := 0
	record, token, authErr := authenticateBootstrapCredential(context.Background(), user, attempt, func(_ context.Context, method, path, token string, body any) ([]byte, error) {
		calls++
		if calls == 1 && method == http.MethodPost && path == "/api/v1/auth/login" && token == "" {
			return []byte(`{"token":"synthetic"}`), nil
		}
		if calls == 2 && method == http.MethodGet && path == "/api/v1/controller" && token == "synthetic" {
			return []byte(`{"controller_id":"11111111-1111-4111-8111-111111111111"}`), nil
		}
		return nil, ErrBoundary
	})
	if authErr != nil || calls != 2 || token != "synthetic" || record.UserID != "" {
		t.Fatal("actual authentication record contract", authErr)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	var raw [5][]byte
	raw[1], err = x509.CreateCertificate(rand.Reader, cert, cert, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	raw[2], _ = json.Marshal(attempt)
	raw[3], _ = json.Marshal(record)
	raw[4], _ = json.Marshal(user)
	review := stockReadAuthReview{"stock-read-auth-binding/v1", id, stockBindingDigest(raw[1]), stockBindingDigest(raw[2]), stockBindingDigest(raw[3])}
	raw[0], _ = json.Marshal(review)
	return id, raw
}
func TestStockReadAuthBindingSyntheticLineage(t *testing.T) {
	id, raw := stockAuthFixture(t)
	binding, err := validateStockReadAuthBinding(id, raw)
	if err != nil {
		t.Fatal(err)
	}
	if binding.record.GarmID != id || binding.review.LeafSHA256 != stockBindingDigest(raw[1]) {
		t.Fatal("binding lost")
	}
	// Each missing or changed input fails. The validator has no exchange, write,
	// filesystem, token, or credential-return path.
	for i := range raw {
		missing := raw
		missing[i] = nil
		if _, err := validateStockReadAuthBinding(id, missing); err == nil {
			t.Fatalf("missing %d", i)
		}
		changed := raw
		changed[i] = append(append([]byte{}, raw[i]...), ' ')
		if _, err := validateStockReadAuthBinding(id, changed); err == nil {
			t.Fatalf("changed %d", i)
		}
	}
	if _, err := validateStockReadAuthBinding(strings.Repeat("c", 64), raw); err == nil {
		t.Fatal("foreign container")
	}
	for _, field := range []string{"GarmID", "CredentialDigest", "ControllerID", "UserID"} {
		altered := raw
		var record map[string]any
		_ = json.Unmarshal(raw[3], &record)
		record[field] = "invalid"
		altered[3], _ = json.Marshal(record)
		var review stockReadAuthReview
		_ = json.Unmarshal(raw[0], &review)
		review.RecordSHA256 = stockBindingDigest(altered[3])
		altered[0], _ = json.Marshal(review)
		if _, err := validateStockReadAuthBinding(id, altered); err == nil {
			t.Fatal("reviewed invalid lineage", field)
		}
	}
}
func TestStockReadAuthBindingRejectsUnsafeFilesWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("fifo", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ordinary"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing", "fifo", "link", "ordinary"} {
		done := make(chan error, 1)
		go func() { _, err := stockReadAuthFile(root, name); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("unsafe file accepted", name)
			}
		case <-time.After(time.Second):
			t.Fatal("blocked read", name)
		}
	}
	p, _ := stockReadFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []context.Context{nil, ctx} {
		if _, err := loadStockReadAuthBinding(c, p); err == nil {
			t.Fatal("invalid context accepted")
		}
	}
}

func TestStockReadAuthBindingRejectsFabricatedUserID(t *testing.T) {
	id, raw := stockAuthFixture(t)
	var record bootstrapRecord
	_ = json.Unmarshal(raw[3], &record)
	record.UserID = "22222222-2222-4222-8222-222222222222"
	raw[3], _ = json.Marshal(record)
	var review stockReadAuthReview
	_ = json.Unmarshal(raw[0], &review)
	review.RecordSHA256 = stockBindingDigest(raw[3])
	raw[0], _ = json.Marshal(review)
	if _, err := validateStockReadAuthBinding(id, raw); err == nil {
		t.Fatal("fabricated current-user identity accepted")
	}
}
