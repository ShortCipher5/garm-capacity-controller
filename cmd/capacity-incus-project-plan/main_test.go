package main

import (
	"bytes"
	"encoding/json"
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

const fictionalProjectState = `{
  "project": "fixture-runners",
  "response": {
    "type":"sync","status":"Success","status_code":200,"operation":"","error_code":0,"error":"",
    "metadata":{"resources":{
      "instances":{"Limit":6,"Usage":0},"containers":{"Limit":5,"Usage":0},"virtual-machines":{"Limit":0,"Usage":0},
      "cpu":{"Limit":12,"Usage":0},"memory":{"Limit":25769803776,"Usage":0},"disk":{"Limit":128849018880,"Usage":0},
      "networks":{"Limit":-1,"Usage":1},"processes":{"Limit":-1,"Usage":0}
    }}
  }
}`

func captureWithState(t *testing.T, state string, v2 bool) string {
	t.Helper()
	raw := strings.TrimSpace(fixture(t))
	if v2 {
		raw = strings.Replace(raw, "incus-project-plan-capture/v1", "incus-project-plan-capture/v2", 1)
	}
	if state != "" {
		raw = strings.TrimSuffix(raw, "}") + `,"project_state":` + state + "}"
	}
	return raw
}

func TestProjectPlanV2RequiresMatchingSuppliedCapsAndReportedEmptyBaseline(t *testing.T) {
	busy := strings.NewReplacer(
		`"instances":{"Limit":6,"Usage":0}`, `"instances":{"Limit":6,"Usage":1}`,
		`"containers":{"Limit":5,"Usage":0}`, `"containers":{"Limit":5,"Usage":1}`,
		`"cpu":{"Limit":12,"Usage":0}`, `"cpu":{"Limit":12,"Usage":2}`,
		`"memory":{"Limit":25769803776,"Usage":0}`, `"memory":{"Limit":25769803776,"Usage":2147483648}`,
		`"disk":{"Limit":128849018880,"Usage":0}`, `"disk":{"Limit":128849018880,"Usage":12884901888}`,
	).Replace(fictionalProjectState)
	for _, tc := range []struct {
		name, state, status string
		code                int
	}{
		{"matched reported zero with standalone network", fictionalProjectState, "reported_allocations_zero", 0},
		{"standalone network at finite limit", strings.Replace(fictionalProjectState, `"networks":{"Limit":-1,"Usage":1}`, `"networks":{"Limit":1,"Usage":1}`, 1), "reported_allocations_zero", 0},
		{"standalone network above finite limit", strings.Replace(fictionalProjectState, `"networks":{"Limit":-1,"Usage":1}`, `"networks":{"Limit":0,"Usage":1}`, 1), "conflicting", 1},
		{"process allocation without instances", strings.Replace(fictionalProjectState, `"processes":{"Limit":-1,"Usage":0}`, `"processes":{"Limit":-1,"Usage":1}`, 1), "conflicting", 1},
		{"process allocation above finite limit", strings.Replace(busy, `"processes":{"Limit":-1,"Usage":0}`, `"processes":{"Limit":10,"Usage":11}`, 1), "conflicting", 1},
		{"reported container", busy, "reported_allocations_nonzero", 1},
		{"reported storage without instances", strings.Replace(fictionalProjectState, `"disk":{"Limit":128849018880,"Usage":0}`, `"disk":{"Limit":128849018880,"Usage":1}`, 1), "reported_allocations_nonzero", 1},
		{"missing effective allocation", strings.Replace(busy, `"memory":{"Limit":25769803776,"Usage":2147483648}`, `"memory":{"Limit":25769803776,"Usage":0}`, 1), "conflicting", 1},
		{"different count limit", strings.Replace(fictionalProjectState, `"Limit":5`, `"Limit":6`, 1), "conflicting", 1},
		{"memory limit differs by one byte", strings.Replace(fictionalProjectState, `25769803776`, `25769803775`, 1), "conflicting", 1},
		{"CPU missing configured cap", strings.Replace(fictionalProjectState, `"Limit":12`, `"Limit":-1`, 1), "conflicting", 1},
		{"contradictory count", strings.Replace(fictionalProjectState, `"containers":{"Limit":5,"Usage":0}`, `"containers":{"Limit":5,"Usage":1}`, 1), "conflicting", 1},
		{"nonzero virtual machine", strings.Replace(fictionalProjectState, `"virtual-machines":{"Limit":0,"Usage":0}`, `"virtual-machines":{"Limit":0,"Usage":1}`, 1), "conflicting", 1},
		{"usage above matched declared cap", strings.Replace(busy, `"cpu":{"Limit":12,"Usage":2}`, `"cpu":{"Limit":12,"Usage":13}`, 1), "conflicting", 1},
		{"operator project label disagrees", strings.Replace(fictionalProjectState, "fixture-runners", "not-to-be-printed", 1), "unknown", 2},
		{"wrong exact key case", strings.Replace(fictionalProjectState, `"Usage":`, `"usage":`, 1), "unknown", 2},
		{"duplicate key", strings.Replace(fictionalProjectState, `"Usage":0`, `"Usage":0,"Usage":0`, 1), "unknown", 2},
		{"omitted usage", strings.Replace(fictionalProjectState, `,"Usage":0`, "", 1), "unknown", 2},
		{"null usage", strings.Replace(fictionalProjectState, `"Usage":0`, `"Usage":null`, 1), "unknown", 2},
		{"fractional usage", strings.Replace(fictionalProjectState, `"Usage":0`, `"Usage":0.0`, 1), "unknown", 2},
		{"unknown extra resource", strings.Replace(fictionalProjectState, `"processes":`, `"not-to-be-printed":{"Limit":0,"Usage":0},"processes":`, 1), "unknown", 2},
		{"missing state", "", "unknown", 2},
		{"null state", "null", "unknown", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			code := run(nil, strings.NewReader(captureWithState(t, tc.state, true)), &out, &diagnostic)
			var got output
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || code != tc.code || got.Schema != "incus-project-plan-report/v2" ||
				got.SuppliedProjectState != tc.status || got.RouteVerified || got.EnforcementVerified ||
				diagnostic.Len() != 0 || strings.Contains(out.String(), "not-to-be-printed") || strings.Contains(out.String(), "fixture-runners") {
				t.Fatalf("code=%d status=%q wanted %d/%s; decode=%v", code, got.SuppliedProjectState, tc.code, tc.status, err)
			}
		})
	}
	// A v1 capture cannot smuggle in an optional state or silently downgrade a v2.
	expect(t, strings.NewReader(captureWithState(t, fictionalProjectState, false)), 2, "unknown", "unknown", "unknown")
	var out, diagnostic bytes.Buffer
	v2BudgetMismatch := strings.Replace(captureWithState(t, fictionalProjectState, true), `"runners": 5`, `"runners": 4`, 1)
	if code := run(nil, strings.NewReader(v2BudgetMismatch), &out, &diagnostic); code != 1 {
		t.Fatalf("zero report overrode modeled cap/budget mismatch: %d", code)
	}
}
