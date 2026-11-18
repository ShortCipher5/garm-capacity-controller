package garmapi

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
	"net/http"
	"strings"
	"testing"
)

func TestManagedDemandAndOwnershipShareActualSource(t *testing.T) {
	controller := "11111111-1111-1111-1111-111111111111"
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("stock bearer escaped")
		}
		w.Header().Set(managedsource.SourceHeader, strings.Repeat("a", 64))
		w.Header().Set(managedsource.ControllerHeader, controller)
		switch r.URL.Path {
		case "/api/v1/pools":
			json.NewEncoder(w).Encode([]any{demandPool(managedPoolID)})
		case "/api/v1/jobs":
			json.NewEncoder(w).Encode([]any{demandJob(1)})
		case "/api/v1/instances":
			w.Write([]byte("[]"))
		case "/api/v1/pools/" + managedPoolID:
			p := demandPool(managedPoolID)
			p["flavor"] = "small"
			p["image"] = "img"
			p["os_type"] = "linux"
			p["os_arch"] = "amd64"
			json.NewEncoder(w).Encode(p)
		default:
			w.WriteHeader(404)
		}
	}))
	trust.Certificates = []tls.Certificate{server.TLS.Certificates[0]}
	sum := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
	pin := hex.EncodeToString(sum[:])
	c, e := NewManagedDemand(server.URL, pin, controller, trust, []string{"capacity"}, []string{managedRepoID})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if c.SourceIdentity() != "" {
		t.Fatal("pre-read identity")
	}
	o, e := c.Observe(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if o.SourceIdentity() == "" || o.SourceIdentity() != c.SourceIdentity() || len(o.Jobs) != 1 {
		t.Fatal("source/demand absent")
	}
	ownership, e := stockidentity.Observe(context.Background(), stockidentity.Configuration{Endpoint: server.URL, ManagedServerKeyPin: pin, ManagedControllerID: controller, TLS: trust, Provider: "capacity", Pool: managedPoolID})
	if e != nil {
		t.Fatal(e)
	}
	if ownership.SourceIdentity() != o.SourceIdentity() || ownership.ControllerID() != controller {
		t.Fatal("mismatched source provenance")
	}
}
