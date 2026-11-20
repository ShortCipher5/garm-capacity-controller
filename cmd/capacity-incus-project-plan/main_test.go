package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

func fixture(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/sanitized-capture.json")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func expect(t *testing.T, in io.Reader, code int, project, host, ceiling string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	got := run(nil, in, &out, &diagnostic)
	want := `{"schema":"incus-project-plan-report/v1","plan_against_project":"` + project +
		`","plan_against_net_host":"` + host + `","project_ceiling_against_net_host":"` + ceiling +
		`","route_verified":false,"enforcement_verified":false}` + "\n"
	if got != code || out.String() != want || diagnostic.Len() != 0 {
		t.Fatal("unexpected exit, report, or diagnostic")
	}
}

func TestProjectPlanCommandModelOnly(t *testing.T) {
	raw := fixture(t)
	expect(t, strings.NewReader(raw), 0, "modeled_fit", "modeled_fit", "modeled_fit")
	// A small mix fits but its project's full ceiling does not.
	underCeiling := strings.Replace(raw, `"runners": 5`, `"runners": 4`, 1)
	expect(t, strings.NewReader(underCeiling), 1, "modeled_fit", "modeled_fit", "modeled_exceeds_budget")
	tooSmall := strings.Replace(raw, `"runners": 5`, `"runners": 2`, 1)
	expect(t, strings.NewReader(tooSmall), 1, "modeled_fit", "modeled_exceeds_budget", "modeled_exceeds_budget")
}

func TestProjectPlanCommandMustNotEchoUnknownInput(t *testing.T) {
	raw := fixture(t)
	for _, m := range []struct{ old, new string }{
		{`"schema":`, `"Schema":`},
		{`"schema":`, `"token":"not-to-be-printed","schema":`},
		{`"id": "fixture-pool"`, `"id":"duplicate","id":"fixture-pool"`},
		{`"count": 3`, `"count": "3"`},
		{`"count": 3`, `"count": 3.0`},
		{`"ram_mib": 24576`, `"ram_mib": null`},
		{`"cpu_millis": 12000`, `"CPU_millis": 12000`},
		{`"description": ""`, `"description":"not-to-be-printed"`},
		{`"used_by": []`, `"used_by":["/1.0/instances/not-to-be-printed"]`},
		{`"limits.cpu": "2"`, `"limits.cpu":"2","limits.cpu":"2"`},
		{`"limits.cpu": "2"`, `"limits.cpu":"3"`},
		{`"response": {`, `"response": null, "excess": {`},
	} {
		mutated := strings.Replace(raw, m.old, m.new, 1)
		if mutated == raw {
			t.Fatal("fixture mutation missing")
		}
		expect(t, strings.NewReader(mutated), 2, "unknown", "unknown", "unknown")
	}
	for _, input := range []io.Reader{
		nil, strings.NewReader(""), strings.NewReader(raw + "{}"),
		strings.NewReader(raw + strings.Repeat(" ", incusapi.Incus702ProjectPlanCaptureMax)),
		io.MultiReader(strings.NewReader(raw), failingReader{}),
	} {
		expect(t, input, 2, "unknown", "unknown", "unknown")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestProjectPlanCommandRefusesArgumentsAndOutputFailure(t *testing.T) {
	var out, diagnostic bytes.Buffer
	if run([]string{"not-to-be-printed"}, failingReader{}, &out, &diagnostic) != 2 ||
		out.Len() != 0 || diagnostic.String() != usage+"\n" {
		t.Fatal("unexpected option handling")
	}
	diagnostic.Reset()
	if run(nil, strings.NewReader(fixture(t)), failingWriter{}, &diagnostic) != 2 ||
		diagnostic.String() != "could not write offline comparison report\n" {
		t.Fatal("write failure not handled")
	}
}
