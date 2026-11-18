//go:build linux

package nativeapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type resourceFixture struct {
	uplink      bool
	objects     map[string]map[string]any
	calls       []string
	posts       int
	consumed    bool
	completed   bool
	fail        string
	preexisting bool
}

func (f *resourceFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	if !f.consumed {
		return nil, errors.New("IO before durable attempt")
	}
	code := 200
	header := http.Header{}
	var metadata any = []string{}
	kind := "sync"
	status := 200
	if r.Method == "POST" {
		f.posts++
		if f.fail == "lost_ack" {
			return nil, errors.New("lost response")
		}
		var obj map[string]any
		if json.NewDecoder(r.Body).Decode(&obj) != nil {
			return nil, errors.New("invalid request")
		}
		target := r.URL.Path + "/" + obj["name"].(string)
		obj["status"] = "Created"
		obj["used_by"] = []string{}
		if r.URL.Path == "/1.0/networks" {
			obj["managed"] = true
		}
		f.objects[target] = obj
		header.Set("Location", target)
		code = 201
		metadata = nil
		if f.fail == "wrong_location" {
			header.Set("Location", "https://elsewhere.invalid/")
		}
		if f.fail == "async" {
			kind = "async"
			status = 100
		}
	} else if obj, ok := f.objects[r.URL.Path]; ok {
		metadata = obj
		if f.fail == "drift" {
			obj["status"] = "Pending"
		}
		if f.fail == "unsafe_config" {
			obj["config"].(map[string]any)["bridge.external_interfaces"] = "host0"
		}
		if f.fail == "used" {
			obj["used_by"] = []string{"/1.0/instances/other"}
		}
	} else if r.URL.Path == "/1.0/networks" {
		metadata = []string{"/1.0/networks/lo"}
		if f.uplink {
			metadata = []string{"/1.0/networks/lo", "/1.0/networks/garm0"}
		}
	} else if r.URL.Path == "/1.0/storage-pools" || r.URL.Path == "/1.0/instances" {
		metadata = nil
	} else {
		code = 404
		kind = "error"
		metadata = nil
		if f.preexisting {
			code = 200
			kind = "sync"
		}
	}
	envelope := map[string]any{"type": kind, "status_code": status, "metadata": metadata}
	if code == 404 {
		envelope["error_code"] = 404
	}
	raw, _ := json.Marshal(envelope)
	if f.fail == "duplicate" {
		raw = []byte(`{"type":"sync","type":"error","status_code":200,"metadata":[]}`)
	}
	return &http.Response{StatusCode: code, Header: header, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
}
func runResources(t *testing.T, f *resourceFixture) error {
	t.Helper()
	p := resourcePolicy(t, nil)
	plan, e := p.PlanFreshResources("tank/owned")
	if e != nil {
		t.Fatal(e)
	}
	guard := func(context.Context) error {
		if f.fail == "scope" {
			return ErrNative
		}
		return nil
	}
	consume := func(string) error {
		if f.consumed {
			return ErrNative
		}
		f.consumed = true
		return nil
	}
	complete := func(string) error {
		if f.fail == "completion" {
			return ErrNative
		}
		f.completed = true
		return nil
	}
	return resourceBootstrap(t.Context(), plan, &http.Client{Transport: f, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, guard, consume, complete)
}
func TestFreshResourceBootstrapConsumesAndReadsBack(t *testing.T) {
	f := &resourceFixture{objects: map[string]map[string]any{}}
	if e := runResources(t, f); e != nil || f.posts != 2 || !f.completed {
		t.Fatalf("%v %+v", e, f)
	}
	before := len(f.calls)
	if runResources(t, f) == nil || len(f.calls) != before {
		t.Fatal("replayed bootstrap")
	}
}
func TestFreshResourceBootstrapNeverAdoptsOrRetriesUnknown(t *testing.T) {
	for _, failure := range []string{"lost_ack", "wrong_location", "async", "drift", "unsafe_config", "used", "duplicate", "scope", "completion", "existing"} {
		t.Run(failure, func(t *testing.T) {
			f := &resourceFixture{objects: map[string]map[string]any{}, fail: failure, preexisting: failure == "existing"}
			if runResources(t, f) == nil || f.completed {
				t.Fatal("unsafe success", f)
			}
			if failure == "existing" && f.posts != 0 {
				t.Fatal("adopted existing")
			}
			for _, call := range f.calls {
				if strings.HasPrefix(call, "DELETE") || strings.HasPrefix(call, "PUT") {
					t.Fatal("rollback/update", call)
				}
			}
			if f.consumed {
				before := len(f.calls)
				if runResources(t, f) == nil || len(f.calls) != before {
					t.Fatal("retried uncertainty")
				}
			}
		})
	}
}
