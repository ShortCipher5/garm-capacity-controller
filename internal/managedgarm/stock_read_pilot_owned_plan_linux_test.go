//go:build linux && stock_read_pilot_harness

package managedgarm

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// A pure fixture ownership planner only. It cannot launch or stop anything,
// prove a receipt's provenance, reserve a path, or establish that cleanup ran.
type stockPilotOwnedPlan struct {
	review                      stockPilotPrelaunchReview
	owned                       [3]stockPilotJoinedLaunchID
	next                        int
	pending, stopped, ambiguous bool
}

var stockPilotOwnedOrder = [...]string{"stock", "ingress", "gateway"}

func newStockPilotOwnedPlan(reviewRaw []byte, reviewPin string, now time.Time, fixtureAbsent bool) (*stockPilotOwnedPlan, error) {
	if !fixtureAbsent || len(reviewRaw) == 0 || len(reviewRaw) > 16384 || !digest.MatchString(reviewPin) || stockBindingDigest(reviewRaw) != reviewPin {
		return nil, ErrBoundary
	}
	review, err := decodeStockPilotPrelaunch(reviewRaw, now)
	if err != nil {
		return nil, ErrBoundary
	}
	return &stockPilotOwnedPlan{review: review}, nil
}

// Pending is set before returning the only descriptor for the next synthetic
// step. Never retry a pending, rejected or ambiguous attempt.
func (p *stockPilotOwnedPlan) begin(now time.Time) (stockPilotLaunchDescriptor, error) {
	if p == nil || p.pending || p.stopped || p.next == len(stockPilotOwnedOrder) {
		return stockPilotLaunchDescriptor{}, ErrBoundary
	}
	expires, err := time.Parse(time.RFC3339, p.review.ExpiresAt)
	if now.IsZero() || err != nil || !expires.After(now) {
		return stockPilotLaunchDescriptor{}, ErrBoundary
	}
	candidate := stockPilotFixtureStockLaunch()
	if p.next > 0 {
		peers, err := stockPilotFixturePeerLaunches(p.owned[0].ContainerID, p.review.GatewayImage, p.review.IngressImage)
		if err != nil {
			return stockPilotLaunchDescriptor{}, ErrBoundary
		}
		// Constructor returns gateway/ingress; lifecycle expects ingress/gateway.
		candidate = peers[2-p.next]
	}
	if candidate.Role != stockPilotOwnedOrder[p.next] {
		return stockPilotLaunchDescriptor{}, ErrBoundary
	}
	p.pending = true
	return candidate, nil
}

// A receipt enters the candidate record only for the expected pending role.
// Current observations must still be independently checked with the existing
// 3-way identity binding; they never become ownership receipts.
func (p *stockPilotOwnedPlan) record(receipt stockPilotJoinedLaunchID) error {
	if p == nil || !p.pending || p.stopped {
		return ErrBoundary
	}
	ok := receipt.Role == stockPilotOwnedOrder[p.next] && digest.MatchString(receipt.ContainerID)
	for _, r := range p.owned[:p.next] {
		if r.ContainerID == receipt.ContainerID {
			ok = false
		}
	}
	if !ok {
		p.fail(false)
		return ErrBoundary
	}
	p.owned[p.next] = receipt
	p.next++
	p.pending = false
	return nil
}

// A definitive no-create result allows review of already recorded ownership
// only. An ambiguous result never permits cleanup by guessed ID/name/path.
func (p *stockPilotOwnedPlan) fail(definitiveNoCreate bool) {
	if p == nil || !p.pending || p.stopped {
		return
	}
	p.pending = false
	p.stopped = true
	p.ambiguous = !definitiveNoCreate
}

// This is a closed, read-only ownership list for follow-up review, not an
// instruction or authorization to remove anything. In-flight or ambiguous
// outcomes withhold even the owned list until adjudicated outside this model.
func (p *stockPilotOwnedPlan) cleanupReview() ([]stockPilotJoinedLaunchID, error) {
	if p == nil || p.pending || p.ambiguous || (!p.stopped && p.next != len(stockPilotOwnedOrder)) {
		return nil, ErrBoundary
	}
	out := make([]stockPilotJoinedLaunchID, 0, p.next)
	for i := p.next - 1; i >= 0; i-- {
		out = append(out, p.owned[i])
	}
	return out, nil
}

func TestStockPilotOwnedPlanKeepsFixedOrderAndCleanupIDs(t *testing.T) {
	raw, _, now := pilotStageJSONFixture(t)
	pin := stockBindingDigest(raw)
	p, err := newStockPilotOwnedPlan(raw, pin, now, true)
	if err != nil {
		t.Fatal(err)
	}
	input := []stockPilotJoinedLaunchID{{"stock", strings.Repeat("a", 64)}, {"ingress", strings.Repeat("b", 64)}, {"gateway", strings.Repeat("c", 64)}}
	for i, r := range input {
		d, err := p.begin(now)
		if err != nil || d.Role != r.Role || !d.ReadOnlyRoot {
			t.Fatal("fixed descriptor refused", i)
		}
		if i == 0 && d.Network != "none" || i > 0 && d.Network != "container:"+input[0].ContainerID {
			t.Fatal("unbound network description")
		}
		if _, err := p.begin(now); err == nil {
			t.Fatal("second launch accepted while pending")
		}
		if _, err := p.cleanupReview(); err == nil {
			t.Fatal("cleanup accepted on pending launch")
		}
		if err := p.record(r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.begin(now); err == nil {
		t.Fatal("extra role accepted")
	}
	out, err := p.cleanupReview()
	if err != nil || !reflect.DeepEqual(out, []stockPilotJoinedLaunchID{input[2], input[1], input[0]}) {
		t.Fatal("wrong recorded cleanup order")
	}
	out[0].ContainerID = input[0].ContainerID
	again, _ := p.cleanupReview()
	if again[0] != input[2] {
		t.Fatal("caller mutated recorded ownership")
	}
}

func TestStockPilotOwnedPlanRejectsAdoptionAndAmbiguity(t *testing.T) {
	raw, _, now := pilotStageJSONFixture(t)
	pin := stockBindingDigest(raw)
	if _, err := newStockPilotOwnedPlan(raw, pin, now, false); err == nil {
		t.Fatal("existing fixture accepted")
	}
	if _, err := newStockPilotOwnedPlan(raw, strings.Repeat("a", 64), now, true); err == nil {
		t.Fatal("unreviewed input accepted")
	}
	for _, tc := range []struct {
		name       string
		bad        stockPilotJoinedLaunchID
		definitive bool
	}{
		{"no create", stockPilotJoinedLaunchID{}, true},
		{"unconfirmed", stockPilotJoinedLaunchID{}, false},
		{"different role", stockPilotJoinedLaunchID{"gateway", strings.Repeat("b", 64)}, false},
		{"adopt stock", stockPilotJoinedLaunchID{"ingress", strings.Repeat("a", 64)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := newStockPilotOwnedPlan(raw, pin, now, true)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.begin(now.Add(time.Hour)); err == nil {
				t.Fatal("expired review accepted")
			}
			stock := stockPilotJoinedLaunchID{"stock", strings.Repeat("a", 64)}
			if _, err := p.begin(now); err != nil || p.record(stock) != nil {
				t.Fatal("stock fixture refused")
			}
			if _, err := p.begin(now); err != nil {
				t.Fatal("peer fixture refused")
			}
			if tc.bad.Role != "" {
				if p.record(tc.bad) == nil {
					t.Fatal("bad receipt accepted")
				}
			} else {
				p.fail(tc.definitive)
			}
			if _, err := p.begin(now); err == nil {
				t.Fatal("retry accepted after failure")
			}
			owned, err := p.cleanupReview()
			if tc.definitive {
				if err != nil || !reflect.DeepEqual(owned, []stockPilotJoinedLaunchID{stock}) {
					t.Fatal("cleanup includes unowned role")
				}
			} else if err == nil || owned != nil {
				t.Fatal("ambiguous cleanup not withheld")
			}
		})
	}
}
