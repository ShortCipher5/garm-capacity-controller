package scalingadvice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func fixture() (Record, Observation, time.Time) {
	now := time.Unix(1000, 0).UTC()
	r := Record{"proposal-1", "source-1", "pool-1", "repo-1", "fingerprint-1", [32]byte{1}, 2, 1, 3, 4, now.Add(-time.Second), now.Add(59 * time.Second), KnownAdvice}
	o := Observation{r.Source, r.PoolID, r.RepositoryID, r.Fingerprint, r.Scope, r.Sequence, r.Revision, r.Observed, now, now.Add(time.Minute)}
	return r, o, now
}
func TestHistoryRoundTripIsNotReplayOrFreshAuthority(t *testing.T) {
	r, o, now := fixture()
	s, e := New().Remember(r)
	if e != nil {
		t.Fatal(e)
	}
	a := s.Assess(r.ID, o, now)
	if a.PlanningBlocked || a.ReplayAllowed || a.Reason != "unchanged_advice_only" {
		t.Fatal(a)
	}
	b, e := s.Encode()
	if e != nil {
		t.Fatal(e)
	}
	restored, e := Decode(b)
	if e != nil {
		t.Fatal(e)
	}
	if a = restored.Assess(r.ID, o, now); !a.PlanningBlocked || a.ReplayAllowed || a.Reason != "restored_history_requires_review" {
		t.Fatal(a)
	}
	r.ID = "new"
	r.PoolID = "new-pool"
	if _, e = restored.Remember(r); e == nil {
		t.Fatal("restored advice accepted new planning")
	}
	clone := s.Records()
	clone[0].Certainty = Uncertain
	if s.Records()[0].Certainty != KnownAdvice {
		t.Fatal("mutable alias")
	}
}
func TestUncertaintyCannotClearFromTargetSnapshot(t *testing.T) {
	r, o, now := fixture()
	s, _ := New().Remember(r)
	uncertain, e := s.MarkUncertain(r.ID)
	if e != nil {
		t.Fatal(e)
	}
	if s.Records()[0].Certainty != KnownAdvice {
		t.Fatal("changed original")
	}
	o.Maximum = r.Suggested
	for _, state := range []State{s, uncertain} {
		if a := state.Assess(r.ID, o, now); !a.PlanningBlocked || a.ReplayAllowed {
			t.Fatal(a)
		}
	}
	b, _ := uncertain.Encode()
	restored, e := Decode(b)
	if e != nil {
		t.Fatal(e)
	}
	if a := restored.Assess(r.ID, o, now); a.Reason != "uncertain_proposal_requires_review" || !a.PlanningBlocked {
		t.Fatal(a)
	}
	r.ID = "other"
	if _, e := uncertain.Remember(r); e == nil {
		t.Fatal("overwrote pool history")
	}
}
func TestAssessmentVetoesConflicts(t *testing.T) {
	r, o, now := fixture()
	s, _ := New().Remember(r)
	for _, change := range []func(*Observation){
		func(o *Observation) { o.Scope[0]++ }, func(o *Observation) { o.Source = "other" }, func(o *Observation) { o.RepositoryID = "other" }, func(o *Observation) { o.PoolID = "other" }, func(o *Observation) { o.Fingerprint = "other" }, func(o *Observation) { o.Sequence++ }, func(o *Observation) { o.Revision++ }, func(o *Observation) { o.Maximum++ }, func(o *Observation) { o.ObservedAt = now.Add(time.Second) }, func(o *Observation) { o.ExpiresAt = now }, func(o *Observation) { o.ObservedAt = time.Time{} },
	} {
		bad := o
		change(&bad)
		if a := s.Assess(r.ID, bad, now); !a.PlanningBlocked || a.ReplayAllowed {
			t.Fatal(a)
		}
	}
	if !s.Assess(r.ID, o, r.ExpiresAt).PlanningBlocked || !s.Assess("unknown", o, now).PlanningBlocked {
		t.Fatal("missing/expired accepted")
	}
}
func TestStrictDecoder(t *testing.T) {
	r, _, _ := fixture()
	s, _ := New().Remember(r)
	raw, _ := s.Encode()
	for _, bad := range [][]byte{
		nil, []byte(`null`), []byte(`{"version":"scaling-advice/v1","records":null}`), append(bytes.Clone(raw), ' '),
		bytes.Replace(raw, []byte(`"version":`), []byte(`"Version":`), 1),
		bytes.Replace(raw, []byte(`"records":`), []byte(`"extra":true,"records":`), 1),
		bytes.Replace(raw, []byte(`"sequence":2`), []byte(`"sequence":2,"sequence":2`), 1),
		bytes.Replace(raw, []byte(`"observed":3`), []byte(`"observed":"3"`), 1),
		bytes.Replace(raw, []byte(`"observed":3`), []byte(`"observed":9223372036854775808`), 1),
		bytes.Replace(raw, []byte(`"certainty":"known_advice"`), []byte(`"certainty":"applied"`), 1),
		[]byte(strings.Repeat("x", MaxStateBytes+1)),
	} {
		if _, e := Decode(bad); e == nil {
			t.Fatalf("accepted invalid bytes: %q", bad)
		}
	}
}
func TestBoundedHistoryAndRecordValidation(t *testing.T) {
	r, _, _ := fixture()
	s := New()
	for i := 0; i < MaxRecords; i++ {
		r.ID = fmt.Sprintf("id-%d", i)
		r.PoolID = fmt.Sprintf("pool-%d", i)
		var e error
		s, e = s.Remember(r)
		if e != nil {
			t.Fatal(e)
		}
	}
	r.ID = "extra"
	r.PoolID = "extra"
	if _, e := s.Remember(r); e == nil {
		t.Fatal("overflow")
	}
	for _, change := range []func(*Record){func(r *Record) { r.ID = "" }, func(r *Record) { r.Source = "bad\nsource" }, func(r *Record) { r.Observed = -1 }, func(r *Record) { r.Suggested = r.Observed }, func(r *Record) { r.Suggested = 0 }, func(r *Record) { r.Sequence = 0 }, func(r *Record) { r.ExpiresAt = r.ObservedAt }, func(r *Record) { r.Certainty = "applied" }} {
		bad, _, _ := fixture()
		change(&bad)
		if _, e := New().Remember(bad); e == nil {
			t.Fatal("invalid record accepted")
		}
	}
	if _, e := New().MarkUncertain("missing"); e == nil {
		t.Fatal("missing proposal accepted")
	}
}

func TestRecordPlanWireBounds(t *testing.T) {
	r, _, _ := fixture()
	for _, change := range []func(*Record){
		func(r *Record) { r.Observed = 0; r.Suggested = 1 },
		func(r *Record) { r.Suggested = r.Observed + 2 },
		func(r *Record) { r.Observed = 4; r.Suggested = 2 },
		func(r *Record) { r.ExpiresAt = r.ObservedAt.Add(MaxObservationLifetime + time.Nanosecond) },
	} {
		bad := r
		change(&bad)
		if _, err := New().Remember(bad); err == nil {
			t.Fatal("invalid plan admitted to history")
		}
		// Canonical bytes alone must not bypass the semantic wire invariants.
		raw, err := json.Marshal(wireState{"scaling-advice/v1", []Record{bad}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Decode(raw); err == nil {
			t.Fatal("malformed plan restored")
		}
	}
	for _, delta := range []int64{-1, 1} {
		good := r
		good.Suggested = good.Observed + delta
		good.ExpiresAt = good.ObservedAt.Add(MaxObservationLifetime)
		state, err := New().Remember(good)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := state.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Decode(raw); err != nil {
			t.Fatal(err)
		}
	}
	state, _ := New().Remember(r)
	_, o, now := fixture()
	o.ExpiresAt = o.ObservedAt.Add(MaxObservationLifetime + time.Nanosecond)
	if a := state.Assess(r.ID, o, now); !a.PlanningBlocked || a.Reason != "observation_missing_or_stale" {
		t.Fatal(a)
	}
}
