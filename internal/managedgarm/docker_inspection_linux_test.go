package managedgarm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type inspectionRoundTrip func(*http.Request) (*http.Response, error)

func (f inspectionRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fakeInspection(t *testing.T, status int, body string, paths *[]string) *dockerInspectionReader {
	t.Helper()
	return &dockerInspectionReader{client: &http.Client{Transport: inspectionRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Host != "docker" || r.URL.Scheme != "http" || r.Body != nil || r.URL.RawQuery != "" {
			t.Fatal("unexpected request", r)
		}
		*paths = append(*paths, r.URL.Path)
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}}
}
func TestInspectionReaderExactPathsAndDecode(t *testing.T) {
	id := strings.Repeat("a", 64)
	paths := []string{}
	d := fakeInspection(t, 200, `{"ID":"`+id+`","State":{"Running":true,"Pid":42}}`, &paths)
	v, e := d.inspect(context.Background(), id)
	if e != nil || v.ID != id || !v.State.Running || v.State.Pid != 42 {
		t.Fatal(v, e)
	}
	d = fakeInspection(t, 200, `[{"Id":"`+id+`"}]`, &paths)
	rows, e := d.running(context.Background())
	if e != nil || len(rows) != 1 || rows[0].Id != id {
		t.Fatal(rows, e)
	}
	if strings.Join(paths, ",") != "/containers/"+id+"/json,/containers/json" {
		t.Fatal(paths)
	}
}
func TestInspectionReaderRejectsRouteExpansionBeforeTransport(t *testing.T) {
	paths := []string{}
	d := fakeInspection(t, 200, `{}`, &paths)
	for _, id := range []string{"", "../x", strings.Repeat("A", 64), strings.Repeat("a", 64) + "?x=1"} {
		if _, e := d.inspect(context.Background(), id); e == nil {
			t.Fatal(id)
		}
	}
	for _, path := range []string{"/containers/create", "/containers/json?all=1", "/images/json", "/containers/" + strings.Repeat("a", 64) + "/start", "http://example.invalid"} {
		if d.get(context.Background(), path, new(any)) == nil {
			t.Fatal(path)
		}
	}
	if len(paths) != 0 {
		t.Fatal(paths)
	}
}
func TestInspectionReaderBoundsAndDecodeCompatibility(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, v := range []struct {
		status int
		body   string
	}{{403, `{}`}, {302, `{}`}, {200, `{`}, {200, `{} {}`}, {200, strings.Repeat(" ", 4<<20) + `{}`}} {
		paths := []string{}
		d := fakeInspection(t, v.status, v.body, &paths)
		if _, e := d.inspect(context.Background(), id); e == nil {
			t.Fatal("accepted invalid response")
		}
	}
	for _, count := range []int{1000, 1001} {
		rows := make([]runningContainer, count)
		raw, _ := json.Marshal(rows)
		paths := []string{}
		d := fakeInspection(t, 200, string(raw), &paths)
		got, e := d.running(context.Background())
		if count == 1000 && (e != nil || len(got) != count) {
			t.Fatal(e)
		}
		if count == 1001 && e == nil {
			t.Fatal("unbounded list")
		}
	}
}
func TestInspectionReaderPreservesCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	d := &dockerInspectionReader{client: &http.Client{Transport: inspectionRoundTrip(func(r *http.Request) (*http.Response, error) {
		got, ok := r.Context().Deadline()
		if !ok || got != deadline {
			t.Fatal("lost deadline")
		}
		return nil, context.DeadlineExceeded
	})}}
	if _, e := d.running(ctx); e == nil {
		t.Fatal("accepted transport failure")
	}
}
