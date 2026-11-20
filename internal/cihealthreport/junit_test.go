package cihealthreport

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

var fixtureBinding = ArtifactBinding{
	Attempt:    cihealth.AttemptKey{Source: "source_a", RepositoryID: 9007199254740993, WorkflowID: 20, RunID: 31, Attempt: 2},
	ArtifactID: 9007199254740997,
}

func TestJUnitProjectionExcludesLogsPathsAndUnsupportedTrust(t *testing.T) {
	xml := []byte(`<?xml version="1.0" encoding="utf-8"?>
<testsuites tests="5" failures="1" errors="1" skipped="1">
  <testsuite name="Outer" tests="5">
    <properties><property name="secret_key" value="hidden_value"/></properties>
    <system-out><![CDATA[log contents that should not be retained]]></system-out>
    <testsuite name="Inner" tests="5" failures="1" errors="1" skipped="1">
      <testcase name="happy [1]" classname="demo.TestCase" time="0.012345678" file="/var/demo/private.go" line="10"/>
      <testcase name="happy [1]" classname="demo.TestCase" time="0.001">
        <failure message="failure data" type="diagnostic">private error traceback</failure>
      </testcase>
      <testcase name="runner" classname="C:\path\to\private" time="2">
        <error message="omit">omit error</error>
      </testcase>
      <testcase name="not available" time="604800"><skipped message="omit"/></testcase>
      <testcase name="&lt;script&gt;" classname="C:private">
        <system-err>omit stderr</system-err>
      </testcase>
    </testsuite>
  </testsuite>
</testsuites>`)
	r, err := ParseJUnit(xml, fixtureBinding)
	if err != nil {
		t.Fatal(err)
	}
	cases := r.Cases()
	want := []Case{
		{Occurrence: 1, Outcome: Pass, Timed: true, Duration: 12_345_678},
		{Occurrence: 2, Outcome: Failure, Timed: true, Duration: time.Millisecond},
		{Occurrence: 3, Outcome: Error, Timed: true, Duration: 2 * time.Second},
		{Occurrence: 4, Outcome: Skipped},
		{Occurrence: 5, Outcome: Pass},
	}
	if !reflect.DeepEqual(cases, want) {
		t.Fatalf("case data: %+v", cases)
	}
	if s := r.Summary(); s != (Summary{Totals: Totals{Cases: 5, Passed: 2, Failed: 1, Errors: 1, Skipped: 1},
		Timed: 3, Mean: 671_115_226, P95: 2 * time.Second}) {
		t.Fatalf("summary: %+v", s)
	}
	binding := r.Binding()
	if binding.Job != nil || binding.Retry != nil || binding.Shard != "" || binding.ArtifactID != 9007199254740997 {
		t.Fatalf("invented provenance: %+v", binding)
	}
	// No retained diagnostics or paths; the returned case slice is isolated.
	for _, undesired := range []string{"Inner", "Outer", "demo.TestCase", "happy [1]", "traceback", "hidden_value", "private.go", "<script>", "system-err", "C:private"} {
		if strings.Contains(fmt.Sprint(r), undesired) {
			t.Fatalf("retained diagnostic material")
		}
	}
	cases[0].Occurrence = 177
	if got := r.Cases()[0].Occurrence; got != want[0].Occurrence {
		t.Fatalf("mutable case projection: %d", got)
	}
}

func TestJUnitNeverRetainsPlausibleFilenameOrTokenLabel(t *testing.T) {
	const (
		groupLabel = "ReleaseSigning"
		suiteLabel = "production-signing-key.pem"
		classLabel = "customer_credentials.env"
		caseLabel  = "ghp_FixtureTokenShapeNotAnActualSecret1234567"
	)
	raw := fmt.Sprintf(`<testsuites name=%q><testsuite name=%q><testcase name=%q classname=%q time="1.2"/></testsuite></testsuites>`,
		groupLabel, suiteLabel, caseLabel, classLabel)
	report, err := ParseJUnit([]byte(raw), fixtureBinding)
	if err != nil {
		t.Fatal(err)
	}
	for _, withheld := range []string{groupLabel, suiteLabel, classLabel, caseLabel} {
		if strings.Contains(fmt.Sprintf("%#v", report), withheld) {
			t.Fatal("retained uncontrolled label")
		}
	}
	if c := report.Cases(); len(c) != 1 || c[0] != (Case{Occurrence: 1, Outcome: Pass, Timed: true, Duration: 1200 * time.Millisecond}) {
		t.Fatalf("redaction lost non-label evidence: %+v", c)
	}
}

func TestJUnitPreservesCallerBoundJobRetryAndShardWithoutCoalescing(t *testing.T) {
	xml := []byte(`<testsuite tests="2"><testcase name="case" time="0"/><testcase name="case"><failure/></testcase></testsuite>`)
	orig := fixtureBinding
	r0, unknown := uint16(0), uint16(4)
	job := cihealth.JobKey{Attempt: orig.Attempt, JobID: 42}
	orig.Job, orig.Retry, orig.Shard = &job, &r0, "shard_a"
	r, err := ParseJUnit(xml, orig)
	if err != nil {
		t.Fatal(err)
	}
	other := fixtureBinding
	other.ArtifactID++
	other.Job, other.Retry, other.Shard = &job, &unknown, "shard_b"
	second, err := ParseJUnit(xml, other)
	if err != nil {
		t.Fatal(err)
	}
	job.JobID, r0, unknown = 99, 12, 13
	a, b := r.Binding(), second.Binding()
	if a.ArtifactID == b.ArtifactID || a.Shard != "shard_a" || b.Shard != "shard_b" ||
		*a.Retry != 0 || *b.Retry != 4 || a.Job.JobID != 42 || b.Job.JobID != 42 {
		t.Fatalf("lost bound artifact/partition: %+v %+v", a, b)
	}
	a.Job.JobID, *a.Retry = 67, 11
	if r.Binding().Job.JobID != 42 || *r.Binding().Retry != 0 {
		t.Fatal("mutable report provenance")
	}
	for _, report := range []Report{r, second} {
		cases := report.Cases()
		if len(cases) != 2 || cases[0].Occurrence == cases[1].Occurrence || !cases[0].Timed || cases[1].Timed ||
			report.Summary().Timed != 1 || report.Summary().Mean != 0 || report.Summary().P95 != 0 {
			t.Fatal("ambiguous duplicate names coalesced or unknown conflated with zero")
		}
	}
}

func TestJUnitMalformedUnsupportedOrMisattributedReportFailsClosed(t *testing.T) {
	wrap := func(body string) string { return "<testsuite>" + body + "</testsuite>" }
	for name, raw := range map[string]string{
		"empty":             "",
		"invalid utf8":      wrap("<testcase name=\"\xff\"/>"),
		"wrong root":        "<report/>",
		"trailing root":     "<testsuite/><testsuite/>",
		"trailing text":     "<testsuite/>extra",
		"bad markup":        "<testsuite><testcase name=\"test\"></testsuite>",
		"contradiction":     wrap(`<testcase name="t"><error/><failure/></testcase>`),
		"repeated outcome":  wrap(`<testcase name="t"><skipped/><skipped/></testcase>`),
		"hidden outcome":    wrap(`<testcase name="t"><system-err><failure/></system-err></testcase>`),
		"unsupported retry": wrap(`<testcase name="t"><rerunFailure/></testcase>`),
		"unknown outcome":   wrap(`<testcase name="t" status="did-not-run"/>`),
		"self-reported job": wrap(`<testcase name="t" job="100"/>`),
		"nested foreign":    wrap(`<testcase name="t"><x:error xmlns:x="https://invalid.example"/></testcase>`),
		"false counts":      `<testsuite tests="1"><testcase name="one"/><testcase name="two"/></testsuite>`,
		"bad counts":        `<testsuite tests="-1"/>`,
		"disabled counts":   `<testsuite disabled="1"><testcase name="one"/></testsuite>`,
		"duplicate attrs":   wrap(`<testcase name="t" time="1" time="2"/>`),
		"no name":           wrap(`<testcase time="1"/>`),
		"blank name":        wrap(`<testcase name=" "/>`),
		"external DTD":      `<!DOCTYPE testsuite SYSTEM "https://invalid.example/schema"><testsuite/>`,
		"internal DTD":      `<!DOCTYPE testsuite [<!ENTITY foo "bar">]><testsuite/>`,
		"entity use":        wrap(`<testcase name="&unsafe;"/>`),
		"processing inst":   `<?xml-stylesheet href="https://invalid.example/"><testsuite/>`,
		"fake declaration":  `<?xml version="1.0" entity='unsafe'?><testsuite/>`,
		"late declaration":  `<!--comment--><?xml version="1.0"?><testsuite/>`,
		"other encoding":    `<?xml version="1.0" encoding="latin1"?><testsuite/>`,
	} {
		t.Run(name, func(t *testing.T) { expectRejection(t, raw, fixtureBinding) })
	}

	job := cihealth.JobKey{Attempt: fixtureBinding.Attempt, JobID: 2}
	bad := fixtureBinding
	bad.Job = &job
	job.Attempt.Attempt++
	expectRejection(t, "<testsuite/>", bad)
	bad = fixtureBinding
	bad.ArtifactID = 0
	expectRejection(t, "<testsuite/>", bad)
	bad = fixtureBinding
	bad.Shard = "/tmp/synthetic"
	expectRejection(t, "<testsuite/>", bad)
}

func TestJUnitBoundsDecimalPrecisionAndNearestRank(t *testing.T) {
	for _, invalid := range []string{
		"", ".1", "NaN", "Inf", "-1", "+1", "1e-6", "1.", "1.2.3", " 1", "1.1234567890",
		"604800.000000001", "604801", "99999999999999999999999999999999",
	} {
		expectRejection(t, fmt.Sprintf(`<testsuite><testcase name="t" time=%q/></testsuite>`, invalid), fixtureBinding)
	}
	expectRejection(t, strings.Repeat(" ", MaxReportBytes+1), fixtureBinding)
	expectRejection(t, `<testsuite>`+strings.Repeat(`<testcase name="t"/>`, MaxCases+1)+`</testsuite>`, fixtureBinding)
	expectRejection(t, `<testsuites>`+strings.Repeat(`<testsuite/>`, MaxSuites+1)+`</testsuites>`, fixtureBinding)
	expectRejection(t, strings.Repeat("<testsuite>", maxDepth+1)+strings.Repeat("</testsuite>", maxDepth+1), fixtureBinding)
	expectRejection(t, `<testsuite>`+strings.Repeat(`<!-- ignored -->`, maxTokens+1)+`</testsuite>`, fixtureBinding)
	expectRejection(t, `<testsuite foo="`+strings.Repeat("a", maxAttribute+1)+`"/>`, fixtureBinding)
	var runs strings.Builder
	runs.WriteString("<testsuite>")
	for i := 19; i >= 0; i-- {
		fmt.Fprintf(&runs, `<testcase name="t" time="%d"/>`, i)
	}
	runs.WriteString(`<testcase name="` + strings.Repeat("a", maxAttribute) + `"/></testsuite>`)
	r, err := ParseJUnit([]byte(runs.String()), fixtureBinding)
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary().Mean != 9*time.Second+500*time.Millisecond || r.Summary().P95 != 18*time.Second ||
		r.Summary().Timed != 20 || len(r.Cases()) != 21 || r.Cases()[20].Timed {
		t.Fatalf("incorrect percentile or timing: %+v", r.Summary())
	}
}

func expectRejection(t *testing.T, raw string, binding ArtifactBinding) {
	t.Helper()
	r, err := ParseJUnit([]byte(raw), binding)
	if err != ErrReport || len(r.Cases()) != 0 || r.Binding().ArtifactID != 0 {
		t.Fatalf("invalid report returned facts or unredacted error: %+v %v", r, err)
	}
}

func FuzzJUnitHasNoPartialReport(f *testing.F) {
	f.Add([]byte(`<testsuite><testcase name="ok" time="1.2"/></testsuite>`))
	f.Add([]byte(`<!DOCTYPE testsuite><testsuite />`))
	f.Add([]byte{0xff, 0xf0})
	f.Fuzz(func(t *testing.T, raw []byte) {
		report, err := ParseJUnit(raw, fixtureBinding)
		if err != nil {
			if err != ErrReport || report.Binding().ArtifactID != 0 || len(report.Cases()) != 0 {
				t.Fatal("partially retained invalid input")
			}
			return
		}
		summary := report.Summary()
		if summary.Cases > MaxCases || summary.Timed > summary.Cases || summary.Passed+summary.Failed+summary.Errors+summary.Skipped != summary.Cases ||
			len(report.Cases()) != summary.Cases || report.Binding().Job != nil {
			t.Fatal("inconsistent report projection")
		}
	})
}
