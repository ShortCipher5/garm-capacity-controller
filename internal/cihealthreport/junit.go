// Package cihealthreport projects a bounded JUnit XML subset into test-case
// observations. It is parsing only: it neither retrieves artifacts nor verifies
// the producer, repository access, execution result, or test comparability.
package cihealthreport

import (
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

const (
	MaxReportBytes = 1 << 20
	MaxCases       = 2000
	MaxSuites      = 256
	MaxDuration    = 7 * 24 * time.Hour

	maxTokens     = 40_000
	maxDepth      = 8
	maxAttributes = 24
	maxAttribute  = 16 << 10
)

var ErrReport = errors.New("invalid or unsupported JUnit report")
var xmlDeclaration = regexp.MustCompile(`^version[ \t]*=[ \t]*(?:"1\.0"|'1\.0')(?:[ \t]+encoding[ \t]*=[ \t]*(?:"(?i:utf-8)"|'(?i:utf-8)'))?(?:[ \t]+standalone[ \t]*=[ \t]*(?:"(?:yes|no)"|'(?:yes|no)'))?[ \t]*$`)

// ArtifactBinding belongs to the authorized caller, not to the report. Callers
// must independently verify the exact artifact and run attempt before parsing.
// Leave Job, Shard, or Retry unknown unless independently bound; the parser
// never trusts job/shard/retry claims in the report or artifact name.
type ArtifactBinding struct {
	Attempt    cihealth.AttemptKey
	ArtifactID int64
	Job        *cihealth.JobKey
	Shard      string
	Retry      *uint16 // zero is a known initial occurrence, nil is unknown
}

type Outcome string

const (
	Pass    Outcome = "pass"
	Failure Outcome = "failure"
	Error   Outcome = "error"
	Skipped Outcome = "skipped"
)

// Case identifies an occurrence within a particular artifact, not a stable
// across-run test key. Test names, class names and suite names are not retained:
// even syntactically simple labels can contain credentials or filenames.
type Case struct {
	Occurrence int
	Outcome    Outcome
	Timed      bool
	Duration   time.Duration
}

type Totals struct {
	Cases, Passed, Failed, Errors, Skipped int
}

// Summary concerns this report only; a report cannot establish how many tests
// the job ran outside these data. Mean is truncated to whole nanoseconds;
// nearest-rank P95 and Mean are absent whenever Timed is zero.
type Summary struct {
	Totals
	Timed int
	Mean  time.Duration
	P95   time.Duration
}

// Report owns only projected values and a private exact-entry digest for
// comparisons. Input bytes, XML bodies and unused attributes are never
// retained. The digest has no accessor or wire representation.
type Report struct {
	binding ArtifactBinding
	cases   []Case
	summary Summary
	digest  [sha256.Size]byte
}

func (r Report) Binding() ArtifactBinding { return cloneBinding(r.binding) }
func (r Report) Cases() []Case            { return append([]Case(nil), r.cases...) }
func (r Report) Summary() Summary         { return r.summary }

// Format deliberately does not reveal artifact proof, including the digest.
func (r Report) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "JUnit report: "+strconv.Itoa(len(r.cases))+" cases")
}

// ParseJUnit parses a deliberately narrow JUnit subset: a testsuite or a
// testsuites group, optionally nested suites, and testcase elements. An
// outcome is either the single direct failure/error/skipped marker or pass
// absent any marker. Unsupported structures and ambiguous outcomes reject
// the entire report; there is no partial success. Standard diagnostic elements
// are disregarded, never interpreted as markup or preserved as text.
func ParseJUnit(raw []byte, binding ArtifactBinding) (Report, error) {
	fail := func() (Report, error) { return Report{}, ErrReport }
	// Check and return the same defensive copy of all reviewed facts. Like
	// raw, pointer fields must not be concurrently mutated by the caller.
	binding = cloneBinding(binding)
	if len(raw) == 0 || len(raw) > MaxReportBytes || !validBinding(binding) {
		return fail()
	}
	// Parse and hash one bounded snapshot of the exact extracted entry,
	// including insignificant XML bytes. Never hash the archive or normalized
	// output as a substitute for these exact report bytes.
	raw = bytes.Clone(raw)
	if !utf8.Valid(raw) {
		return fail()
	}
	d := xml.NewDecoder(bytes.NewReader(raw))
	// Standard XML escaping is handled by the strict decoder. No custom
	// entities, external charset readers, DTDs or stylesheet PIs.
	d.Strict = true
	p := parser{stack: make([]frame, 0, maxDepth), cases: make([]Case, 0, 32)}
	for n := 0; n < maxTokens; n++ {
		token, err := d.Token()
		if err == io.EOF {
			if !p.closed || len(p.stack) != 0 {
				return fail()
			}
			return Report{binding: binding, cases: p.cases, summary: summarize(p.cases, p.totals), digest: sha256.Sum256(raw)}, nil
		}
		if err != nil || !p.consume(token) {
			return fail()
		}
	}
	return fail()
}

type kind uint8

const (
	group kind = iota + 1
	suite
	testcase
	failNode
	errorNode
	skipNode
	output
	properties
	property
)

type frame struct {
	kind    kind
	totals  Totals
	checks  []declared
	outcome bool
	test    Case
}
type declared struct {
	name  string
	value int
}
type parser struct {
	stack  []frame
	cases  []Case
	totals Totals
	suites int
	opened bool
	closed bool
	decl   bool
	tokens int
}

func (p *parser) consume(token xml.Token) bool {
	p.tokens++
	switch v := token.(type) {
	case xml.StartElement:
		return p.start(v)
	case xml.EndElement:
		return p.end()
	case xml.CharData:
		if len(p.stack) > 0 {
			switch p.stack[len(p.stack)-1].kind {
			case failNode, errorNode, skipNode, output, property:
				return true
			}
		}
		return xmlSpace(v)
	case xml.Comment:
		return true
	case xml.ProcInst:
		if v.Target != "xml" || p.tokens != 1 || p.opened || p.decl || len(v.Inst) > 128 || !xmlDeclaration.Match(v.Inst) {
			return false
		}
		p.decl = true
		return true
	default:
		// This includes directives even when buried in a diagnostic section.
		return false
	}
}

func xmlSpace(text []byte) bool {
	for _, c := range text {
		switch c {
		case ' ', '\t', '\r', '\n':
		default:
			return false
		}
	}
	return true
}

func (p *parser) start(x xml.StartElement) bool {
	if p.closed || len(p.stack) >= maxDepth || x.Name.Space != "" || len(x.Attr) > maxAttributes {
		return false
	}
	k := classify(x.Name.Local)
	parent := kind(0)
	if len(p.stack) > 0 {
		parent = p.stack[len(p.stack)-1].kind
	}
	if !validChild(parent, k) || !validAttributes(x.Attr) {
		return false
	}
	p.opened = true
	f := frame{kind: k}
	switch k {
	case group, suite:
		if k == suite {
			if p.suites >= MaxSuites {
				return false
			}
			p.suites++
		}
		for _, a := range x.Attr {
			if a.Name.Space != "" {
				continue
			}
			switch a.Name.Local {
			case "tests", "failures", "errors", "skipped", "disabled":
				value, ok := unsigned(a.Value, MaxCases)
				if !ok {
					return false
				}
				f.checks = append(f.checks, declared{a.Name.Local, int(value)})
			}
		}
	case testcase:
		if len(p.cases) >= MaxCases {
			return false
		}
		f.test = Case{Outcome: Pass}
		validName := false
		for _, a := range x.Attr {
			if a.Name.Space != "" {
				return false
			}
			switch a.Name.Local {
			case "name":
				validName = strings.TrimSpace(a.Value) != ""
			case "time":
				value, ok := duration(a.Value)
				if !ok {
					return false
				}
				f.test.Timed, f.test.Duration = true, value
			case "classname", "file", "line", "assertions":
				// Not displayed or used for identity/timing.
			default:
				// Do not silently call an unknown status extension a pass.
				return false
			}
		}
		if !validName {
			return false
		}
	case failNode, errorNode, skipNode:
		f := &p.stack[len(p.stack)-1]
		if f.outcome {
			return false
		}
		f.outcome = true
		switch k {
		case failNode:
			f.test.Outcome = Failure
		case errorNode:
			f.test.Outcome = Error
		case skipNode:
			f.test.Outcome, f.test.Timed, f.test.Duration = Skipped, false, 0
		}
	}
	p.stack = append(p.stack, f)
	return true
}

func (p *parser) end() bool {
	if len(p.stack) == 0 {
		return false
	}
	last := len(p.stack) - 1
	f := p.stack[last]
	p.stack[last] = frame{}
	p.stack = p.stack[:last]
	switch f.kind {
	case group, suite:
		for _, count := range f.checks {
			if declaredValue(f.totals, count.name) != count.value {
				return false
			}
		}
	case testcase:
		f.test.Occurrence = len(p.cases) + 1
		p.cases = append(p.cases, f.test)
		f.totals.addCase(f.test.Outcome)
	}
	if len(p.stack) == 0 {
		p.closed, p.totals = true, f.totals
	} else {
		p.stack[len(p.stack)-1].totals.add(f.totals)
	}
	return true
}

func classify(name string) kind {
	switch name {
	case "testsuites":
		return group
	case "testsuite":
		return suite
	case "testcase":
		return testcase
	case "failure":
		return failNode
	case "error":
		return errorNode
	case "skipped":
		return skipNode
	case "system-out", "system-err":
		return output
	case "properties":
		return properties
	case "property":
		return property
	}
	return 0
}

func validChild(parent, child kind) bool {
	switch parent {
	case 0:
		return child == group || child == suite
	case group:
		return child == suite
	case suite:
		return child == suite || child == testcase || child == properties || child == output
	case testcase:
		return child == failNode || child == errorNode || child == skipNode || child == properties || child == output
	case properties:
		return child == property
	}
	return false
}

func validAttributes(attrs []xml.Attr) bool {
	for i, a := range attrs {
		if len(a.Name.Local) > 64 || len(a.Name.Space) > 256 || len(a.Value) > maxAttribute {
			return false
		}
		for _, prior := range attrs[:i] {
			if a.Name == prior.Name {
				return false
			}
		}
	}
	return true
}

func declaredValue(t Totals, name string) int {
	switch name {
	case "tests":
		return t.Cases
	case "failures":
		return t.Failed
	case "errors":
		return t.Errors
	case "skipped":
		return t.Skipped
	case "disabled":
		// "disabled" may differ from "skipped" across producers. Only
		// zero has the semantics supported by this subset.
		return 0
	}
	return -1
}

func (t *Totals) addCase(outcome Outcome) {
	t.Cases++
	switch outcome {
	case Pass:
		t.Passed++
	case Failure:
		t.Failed++
	case Error:
		t.Errors++
	case Skipped:
		t.Skipped++
	}
}

func (t *Totals) add(other Totals) {
	t.Cases += other.Cases
	t.Passed += other.Passed
	t.Failed += other.Failed
	t.Errors += other.Errors
	t.Skipped += other.Skipped
}

func summarize(cases []Case, t Totals) Summary {
	out := Summary{Totals: t}
	times := make([]time.Duration, 0, len(cases))
	var total time.Duration // MaxCases * MaxDuration fits in int64.
	for _, c := range cases {
		if c.Timed {
			times = append(times, c.Duration)
			total += c.Duration
		}
	}
	if len(times) > 0 {
		slices.Sort(times)
		out.Timed = len(times)
		out.Mean = total / time.Duration(out.Timed)
		out.P95 = times[(95*out.Timed+99)/100-1]
	}
	return out
}

func unsigned(s string, max uint64) (uint64, bool) {
	if len(s) == 0 || len(s) > 19 {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, e := strconv.ParseUint(s, 10, 64)
	return n, e == nil && n <= max
}

func duration(s string) (time.Duration, bool) {
	whole, fractional, dot := strings.Cut(s, ".")
	seconds, ok := unsigned(whole, uint64(MaxDuration/time.Second))
	if !ok {
		return 0, false
	}
	total := time.Duration(seconds) * time.Second
	if dot {
		nanos, ok := unsigned(fractional, 999_999_999)
		if !ok || len(fractional) > 9 {
			return 0, false
		}
		for i := len(fractional); i < 9; i++ {
			nanos *= 10
		}
		total += time.Duration(nanos)
	}
	return total, total <= MaxDuration
}

func sourceAtom(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func validBinding(b ArtifactBinding) bool {
	a := b.Attempt
	return b.ArtifactID > 0 && sourceAtom(a.Source) && a.RepositoryID > 0 && a.WorkflowID > 0 &&
		a.RunID > 0 && a.Attempt > 0 && (b.Job == nil || b.Job.JobID > 0 && b.Job.Attempt == a) &&
		(b.Shard == "" || sourceAtom(b.Shard))
}

func cloneBinding(b ArtifactBinding) ArtifactBinding {
	if b.Job != nil {
		v := *b.Job
		b.Job = &v
	}
	if b.Retry != nil {
		v := *b.Retry
		b.Retry = &v
	}
	return b
}
