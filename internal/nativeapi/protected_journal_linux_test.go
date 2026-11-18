//go:build linux

package nativeapi

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedinit"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRootJournalExactReadOnlyMetadataAndBytes(t *testing.T) {
	domain := strings.Repeat("a", 64)
	resource := "11111111-1111-4111-8111-111111111111"
	initHash := strings.Repeat("b", 64)
	for _, mode := range []string{"ok", "writable-parent", "symlink", "wrong-uid", "wrong-incarnation", "fabricated-authority", "noncanonical", "future"} {
		t.Run(mode, func(t *testing.T) {
			record := protectedinit.Record{Domain: domain, Incarnation: resource, LauncherSHA256: initHash, State: "process_tree_exited_unqualified", At: time.Now().UTC(), ExitCode: 0}
			if mode == "wrong-incarnation" {
				record.Incarnation = "22222222-2222-4222-8222-222222222222"
			}
			if mode == "fabricated-authority" {
				record.JobCompletionAuthorized = true
			}
			if mode == "future" {
				record.At = record.At.Add(time.Hour)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/1.0/instances/runner-a/files" {
					t.Error("wrong instance")
				}
				path := r.URL.Query().Get("path")
				w.Header().Set("X-Incus-uid", "0")
				w.Header().Set("X-Incus-gid", "0")
				if strings.HasSuffix(path, "state.json") {
					if r.Method != "GET" {
						t.Error("mutation attempt")
					}
					w.Header().Set("X-Incus-mode", "0600")
					w.Header().Set("X-Incus-type", "file")
					if mode == "wrong-uid" {
						w.Header().Set("X-Incus-uid", "1000")
					}
					if mode == "symlink" {
						w.Header().Set("X-Incus-type", "symlink")
					}
					raw, _ := json.Marshal(record)
					if mode == "noncanonical" {
						raw = append([]byte(" "), raw...)
					}
					w.Write(append(raw, '\n'))
					return
				}
				if r.Method != "HEAD" {
					t.Error("mutation attempt")
				}
				w.Header().Set("X-Incus-mode", "0755")
				w.Header().Set("X-Incus-type", "directory")
				if mode == "writable-parent" {
					w.Header().Set("X-Incus-mode", "0777")
				}
			}))
			defer server.Close()
			client := server.Client()
			original := client.Transport
			client.Transport = journalRewrite{server.URL, original}
			c := &Client{http: client}
			r, e := c.readProtectedJournal(t.Context(), "runner-a", domain, resource, initHash)
			if mode == "ok" {
				if e != nil || r.State != "process_tree_exited_unqualified" {
					t.Fatal(r, e)
				}
			} else if e == nil {
				t.Fatal("untrusted journal accepted")
			}
			if _, e = c.journalRequest(t.Context(), "runner-a", resource, "/etc/shadow"); e == nil {
				t.Fatal("arbitrary path allowed")
			}
		})
	}
}

type journalRewrite struct {
	url  string
	next http.RoundTripper
}

func (t journalRewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	u := *r.URL
	r.URL = &u
	r.URL.Scheme = "http"
	r.URL.Host = strings.TrimPrefix(t.url, "http://")
	return t.next.RoundTrip(r)
}
func TestZeroJournalCannotMintObservation(t *testing.T) {
	var c *Client
	if _, e := c.ObserveProtectedJournal(t.Context(), Prepared{}, ""); e == nil {
		t.Fatal("zero inputs")
	}
	if (ProtectedJournal{}).Valid() {
		t.Fatal("zero observation")
	}
}
