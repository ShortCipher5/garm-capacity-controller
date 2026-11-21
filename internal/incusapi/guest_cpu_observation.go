package incusapi

import (
	"encoding/json"
	"math"
	"strings"
	"time"
)

// v6.0.4 and v7.0.2 both document instance_state_started_at. LXC
// reports its host init process; QEMU reports the host VM process even
// when the CPU count came from the guest. Allow only explicit containers.
func qualifiesGuestCPUContainer(fields map[string]json.RawMessage) bool {
	kind, err := field[string](fields, "type")
	if err != nil || kind != "container" {
		return false
	}
	for key := range fields {
		for _, field := range [...]string{"name", "created_at", "type"} {
			if key != field && strings.EqualFold(key, field) {
				return false
			}
		}
	}
	return true
}

func parseGuestCPUObservation(raw json.RawMessage, guest guestInventory, bound time.Time) GuestCPUObservation {
	var none GuestCPUObservation
	if !guest.container || guest.CreatedAt.IsZero() {
		return none
	}
	fields, err := runnerRootVolumeObject(raw, "status", "status_code", "pid", "started_at", "cpu")
	if err != nil || !runnerHealthRunning(fields) {
		return none
	}
	pid, err := field[int64](fields, "pid")
	if err != nil || pid <= 1 || pid > math.MaxInt32 {
		return none
	}
	started, err := identityTime(fields, "started_at")
	if err != nil || started.Before(guest.CreatedAt) || started.After(bound) {
		return none
	}
	cpuFields, err := runnerHealthCounterObject(fields["cpu"], "usage")
	if err != nil {
		return none
	}
	used, err := field[int64](cpuFields, "usage")
	if err != nil || used <= 0 {
		return none
	}
	return GuestCPUObservation{
		Name: guest.Name, CreatedAt: guest.CreatedAt.UTC(), StartedAt: started, PID: pid, CPUUsedNanos: used,
	}
}
