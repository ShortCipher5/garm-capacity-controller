// Package retirement supplies a read-only assessment of drain blockers.
// It issues no retirement permit and has no external or ledger write capability.
package retirement

import (
	"errors"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

var ErrSnapshot = errors.New("drain assessment unavailable")

type Counts struct {
	Creating int `json:"creating"`
	Idle     int `json:"idle"`
	Running  int `json:"running"`
	Deleting int `json:"deleting"`
	Failed   int `json:"failed"`
	Unknown  int `json:"unknown"`
}

type Assessment struct {
	// LedgerNewClaimsPaused does not cover an already committed dispatch or
	// existing runners' GitHub job assignment. It is NOT a drain-complete flag.
	LedgerNewClaimsPaused bool      `json:"ledger_new_claims_paused"`
	ObservationFresh      bool      `json:"observation_fresh"`
	ObservedAt            time.Time `json:"observed_at"`
	TrackedAllocations    int       `json:"tracked_allocations"`
	States                Counts    `json:"last_observed_states"`
	Blockers              []string  `json:"blockers"`
}

// Assess reads one transactionally consistent ledger snapshot. Counts are last
// observations, not authoritative proof that a runner is currently idle/busy.
// A reservation missing from inventory remains counted. A verified linked
// inventory resource supersedes its reservation's state, never its charge.
// Even an empty, fresh snapshot cannot prove physical/job drain completion.
func Assess(snapshot admission.SQLiteSnapshot, hostID string, now time.Time) (Assessment, error) {
	host, ok := snapshot.Config.Hosts[hostID]
	if !ok || hostID == "" || (host.Mode != "enabled" && host.Mode != "draining" && host.Mode != "disabled") {
		return Assessment{}, ErrSnapshot
	}
	age := now.Sub(host.ObservedAt)
	fresh := !host.ObservedAt.IsZero() && snapshot.Config.MaxObservationAge > 0 && age >= 0 && age <= snapshot.Config.MaxObservationAge
	result := Assessment{
		LedgerNewClaimsPaused: !snapshot.Ready || host.Mode != "enabled" || !host.Healthy || !fresh,
		ObservationFresh:      fresh,
		ObservedAt:            host.ObservedAt,
		Blockers:              []string{},
	}
	if !result.LedgerNewClaimsPaused {
		result.Blockers = append(result.Blockers, "new_ledger_claims_not_paused")
	}
	if !fresh || !host.Healthy {
		result.Blockers = append(result.Blockers, "fresh_healthy_inventory_required")
	}
	linked := map[string]bool{}
	seen := map[string]bool{}
	count := func(state string) {
		result.TrackedAllocations++
		switch state {
		case "creating":
			result.States.Creating++
		case "idle":
			result.States.Idle++
		case "running":
			result.States.Running++
		case "deleting":
			result.States.Deleting++
		case "failed":
			result.States.Failed++
		default:
			result.States.Unknown++
		}
	}
	for _, resource := range snapshot.Inventory {
		if resource.Host != hostID {
			continue
		}
		if resource.ID == "" || seen[resource.ID] {
			return Assessment{}, ErrSnapshot
		}
		seen[resource.ID] = true
		if resource.ReservationKey != "" {
			reservation, exists := snapshot.Reservations[resource.ReservationKey]
			if !exists || linked[resource.ReservationKey] || reservation.Request.Key != resource.ReservationKey || reservation.Request.Host != hostID || reservation.Request.Pool != resource.Pool {
				return Assessment{}, ErrSnapshot
			}
			linked[resource.ReservationKey] = true
		}
		count(resource.State)
	}
	for key, reservation := range snapshot.Reservations {
		if reservation.Request.Host != hostID {
			continue
		}
		if key == "" || key != reservation.Request.Key {
			return Assessment{}, ErrSnapshot
		}
		if !linked[key] {
			count(reservation.State)
		}
	}
	if result.TrackedAllocations != 0 {
		result.Blockers = append(result.Blockers, "tracked_allocations_remain_charged")
	}
	// These are capability gates, not booleans inferred from counts or modes.
	// Never make them disappear merely because a row, operation or runner is absent.
	result.Blockers = append(result.Blockers, "job_assignment_fence_unqualified", "confirmed_retirement_unimplemented")
	return result, nil
}
