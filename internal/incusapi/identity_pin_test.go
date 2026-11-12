package incusapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPinnedHandshakeRejectsRotationBetweenRootAndPost(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		t.Run(map[bool]string{false: "unpinned-negative-control", true: "pinned"}[pinned], func(t *testing.T) {
			donorA, credentials := tlsFixture(t, http.NotFoundHandler())
			donorB, _ := tlsFixture(t, http.NotFoundHandler())
			otherRoot, err := x509.ParseCertificate(donorB.TLS.Certificates[0].Certificate[1])
			if err != nil {
				t.Fatal(err)
			}
			credentials = credentials.Clone()
			credentials.RootCAs = credentials.RootCAs.Clone()
			credentials.RootCAs.AddCert(otherRoot)
			configA := donorA.TLS.Clone()
			configA.GetConfigForClient = nil
			configB := donorB.TLS.Clone()
			configB.ClientCAs = configA.ClientCAs
			configB.GetConfigForClient = nil
			var rotated atomic.Bool
			var roots, posts atomic.Int32
			var server *httptest.Server
			server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/1.0":
					if roots.Add(1) == 2 {
						rotated.Store(true)
						w.Header().Set("Connection", "close")
					}
					writeSync(w, physicalRoot(server))
				case "/1.0/instances":
					posts.Add(1)
					json.NewEncoder(w).Encode(map[string]any{"type": "async", "status_code": 100, "operation": "/1.0/operations/" + operation, "metadata": map[string]any{"id": operation}})
				default:
					http.NotFound(w, r)
				}
			}))
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			server.TLS = configA.Clone()
			server.TLS.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
				if rotated.Load() {
					return configB, nil
				}
				return configA, nil
			}
			server.StartTLS()
			defer server.Close()
			client, err := New(server.URL, "runners", credentials)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			identity, err := client.PhysicalIdentity(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if pinned {
				if err := client.PinPhysicalIdentity(identity); err != nil {
					t.Fatal(err)
				}
			}
			_, err = client.Create(context.Background(), validSpec())
			if roots.Load() != 2 {
				t.Fatal("fixture did not reach root-to-POST window")
			}
			if pinned {
				if err == nil || posts.Load() != 0 {
					t.Fatal("new trusted-CA key received pinned POST")
				}
			} else {
				if err != nil || posts.Load() != 1 {
					t.Fatalf("negative control did not demonstrate unpinned rotation: %v posts%d", err, posts.Load())
				}
			}
		})
	}
}
func TestPinIsOpaqueScopeBoundAndImmutableUnderConcurrency(t *testing.T) {
	var server *httptest.Server
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeSync(w, physicalRoot(server)) }))
	a, err := New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := New(server.URL, "another-project", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	first, err := a.PhysicalIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	other, err := b.PhysicalIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a.PinPhysicalIdentity(PhysicalServerIdentity{}) == nil || a.PinPhysicalIdentity(other) == nil {
		t.Fatal("forged/wrong-project pin accepted")
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.PinPhysicalIdentity(first); err != nil {
				t.Error(err)
			}
			if _, err := a.PhysicalIdentity(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	changed := first
	changed.serverName = "another-daemon"
	if a.PinPhysicalIdentity(changed) == nil {
		t.Fatal("pin was mutable")
	}
}

func TestPinDiscardsPreexistingPoolBeforeAnyWrite(t *testing.T) {
	donorA, credentials := tlsFixture(t, http.NotFoundHandler())
	donorB, _ := tlsFixture(t, http.NotFoundHandler())
	otherRoot, err := x509.ParseCertificate(donorB.TLS.Certificates[0].Certificate[1])
	if err != nil {
		t.Fatal(err)
	}
	credentials = credentials.Clone()
	credentials.RootCAs = credentials.RootCAs.Clone()
	credentials.RootCAs.AddCert(otherRoot)
	configA := donorA.TLS.Clone()
	configA.GetConfigForClient = nil
	configB := donorB.TLS.Clone()
	configB.ClientCAs = configA.ClientCAs
	configB.GetConfigForClient = nil
	var rotated atomic.Bool
	var connections sync.Map
	var writesA, writesB atomic.Int32
	var server *httptest.Server
	server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/1.0":
			writeSync(w, physicalRoot(server))
		case "/warm":
			writeSync(w, map[string]any{})
		case "/1.0/instances":
			value, ok := connections.Load(r.RemoteAddr)
			if !ok {
				t.Error("missing fixture TLS generation")
			} else if value.(bool) {
				writesB.Add(1)
			} else {
				writesA.Add(1)
			}
			json.NewEncoder(w).Encode(map[string]any{"type": "async", "status_code": 100, "operation": "/1.0/operations/" + operation, "metadata": map[string]any{"id": operation}})
		default:
			http.NotFound(w, r)
		}
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = configA.Clone()
	server.TLS.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		generation := rotated.Load()
		connections.Store(hello.Conn.RemoteAddr().String(), generation)
		if generation {
			return configB, nil
		}
		return configA, nil
	}
	server.StartTLS()
	defer server.Close()
	client, err := New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	identity, err := client.PhysicalIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server.CloseClientConnections()
	rotated.Store(true)
	if _, err := client.request(context.Background(), http.MethodGet, "/warm", nil, nil); err != nil {
		t.Fatal(err)
	}
	rotated.Store(false)
	if err := client.PinPhysicalIdentity(identity); err != nil {
		t.Fatal(err)
	}
	// Deliberately exercise the lowest request layer: transport pinning cannot
	// rely on a caller making another root check before sending a request body.
	if _, err := client.request(context.Background(), http.MethodPost, "/1.0/instances", nil, map[string]string{"name": "runner-1"}); err != nil {
		t.Fatal(err)
	}
	if writesA.Load() != 1 || writesB.Load() != 0 {
		t.Fatal("an unpinned pooled connection received a write")
	}
}

func TestPinnedRawIdentityRejectsSameKeyMetadataDrift(t *testing.T) {
	for _, field := range []string{"server_name", "server_version"} {
		t.Run(field, func(t *testing.T) {
			var drift atomic.Bool
			var nonroot atomic.Int32
			var server *httptest.Server
			server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/1.0" {
					nonroot.Add(1)
					writeSync(w, []any{})
					return
				}
				root := physicalRoot(server)
				if drift.Load() {
					root["environment"].(map[string]any)[field] = "changed-metadata"
				}
				writeSync(w, root)
			}))
			pinned, err := New(server.URL, "runners", credentials)
			if err != nil {
				t.Fatal(err)
			}
			defer pinned.Close()
			identity, err := pinned.PhysicalIdentity(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := pinned.PinPhysicalIdentity(identity); err != nil {
				t.Fatal(err)
			}
			drift.Store(true)
			// An independent unbound observation can truthfully be Valid; that is not
			// authorization to replace a previously established durable binding.
			observer, err := New(server.URL, "runners", credentials)
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close()
			observed, err := observer.PhysicalIdentity(context.Background())
			if err != nil || !observed.Valid() || observed == identity {
				t.Fatal("negative control failed to observe changed valid metadata")
			}
			if _, err := pinned.PhysicalIdentity(context.Background()); err == nil {
				t.Fatal("raw getter bypassed established pin")
			}
			if _, err := pinned.Instances(context.Background()); err == nil {
				t.Fatal("inventory bypassed established pin")
			}
			if nonroot.Load() != 0 {
				t.Fatal("metadata drift reached downstream inventory")
			}
		})
	}
}
