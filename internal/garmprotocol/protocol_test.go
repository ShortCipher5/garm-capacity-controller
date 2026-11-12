package garmprotocol

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func testRequest() Request { return Request{Protocol: Version, Name: "runner-1", PoolID: "pool-1"} }

func TestReservedAndDeferredProtocol(t *testing.T) {
	no := false
	request := testRequest()
	for _, response := range []Response{
		{Protocol: Version, Outcome: "reserved", Name: request.Name, PoolID: request.PoolID, Dispatched: &no, ReservationKey: "reservation-1"},
		{Protocol: Version, Outcome: "deferred", Name: request.Name, PoolID: request.PoolID, Dispatched: &no, RetryAfterSeconds: 10},
	} {
		exit := 0
		if response.Outcome == "deferred" {
			exit = DeferredExitCode
		}
		encoded, err := Encode(exit, response, request)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := Parse(exit, encoded, request)
		if err != nil || parsed.Outcome != response.Outcome {
			t.Fatalf("round trip failed: %+v %v", parsed, err)
		}
	}
}

func TestRejectsAmbiguousOrDispatchedResponses(t *testing.T) {
	no := false
	yes := true
	base := Response{Protocol: Version, Outcome: "deferred", Name: "runner-1", PoolID: "pool-1", Dispatched: &no, RetryAfterSeconds: 10}
	cases := map[string]func(*Response){
		"protocol":             func(r *Response) { r.Protocol = "future" },
		"name":                 func(r *Response) { r.Name = "another-runner" },
		"pool":                 func(r *Response) { r.PoolID = "another-pool" },
		"missing dispatch":     func(r *Response) { r.Dispatched = nil },
		"dispatched":           func(r *Response) { r.Dispatched = &yes },
		"retry short":          func(r *Response) { r.RetryAfterSeconds = 0 },
		"retry long":           func(r *Response) { r.RetryAfterSeconds = 61 },
		"reservation on defer": func(r *Response) { r.ReservationKey = "existing" },
		"unknown outcome":      func(r *Response) { r.Outcome = "success" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := base
			mutate(&r)
			encoded, _ := json.Marshal(r)
			if _, err := Parse(DeferredExitCode, encoded, testRequest()); err == nil {
				t.Fatal("invalid defer accepted")
			}
		})
	}
	encoded, _ := json.Marshal(base)
	for _, exit := range []int{0, 1, 30, 31, 74, 76} {
		if _, err := Parse(exit, encoded, testRequest()); err == nil {
			t.Fatalf("wrong exit %d accepted", exit)
		}
	}
}

func TestRejectsMalformedTrailingUnknownAndOversizeOutput(t *testing.T) {
	for _, output := range [][]byte{
		[]byte("not json"),
		[]byte(`{"protocol":"garm-capacity/v1","outcome":"deferred","name":"runner-1","pool_id":"pool-1","dispatched":true,"dispatched":false,"retry_after_seconds":10}`),
		[]byte(`{"Protocol":"garm-capacity/v1","outcome":"deferred","name":"runner-1","pool_id":"pool-1","dispatched":false,"retry_after_seconds":10}`),
		[]byte(`{"protocol":"garm-capacity/v1","outcome":"deferred","name":"runner-1","pool_id":"pool-1","dispatched":false,"retry_after_seconds":10,"secret":"must-not-echo"}`),
		[]byte(`{"protocol":"garm-capacity/v1","outcome":"deferred","name":"runner-1","pool_id":"pool-1","dispatched":false,"retry_after_seconds":10} {}`),
		bytes.Repeat([]byte("x"), MaximumResponseBytes+1),
	} {
		_, err := Parse(DeferredExitCode, output, testRequest())
		if err == nil || strings.Contains(err.Error(), "must-not-echo") {
			t.Fatal("unsafe output accepted or leaked")
		}
	}
}

func TestRejectsMissingOrControlCharacterIdentity(t *testing.T) {
	for _, identity := range []string{"", "bad\nname", strings.Repeat("a", 257)} {
		r := testRequest()
		r.Name = identity
		if r.Validate() == nil {
			t.Fatal("invalid request identity accepted")
		}
	}
}
