package demandrepair

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	ciRateSecondaryFallback = time.Minute
	ciRateMissingResetFloor = time.Hour
	ciRateMaxDelay          = 24 * time.Hour
	ciRateMaxHeaderValues   = 8
)

// ciRateGate retains only a local not-before time and clock observation.
// It does not store headers, credentials or upstream diagnostics. This
// dedicated transport is serial/no-wait and has no retry or wake mechanism.
type ciRateGate struct {
	mu      sync.Mutex
	clock   func() time.Time
	last    time.Time
	until   time.Time
	refused bool
}

func (g *ciRateGate) begin() bool {
	if !g.mu.TryLock() {
		return false
	}
	if !g.permitted() {
		g.mu.Unlock()
		return false
	}
	return true
}

func (g *ciRateGate) permitted() bool {
	now, ok := g.current()
	return !g.refused && ok && !now.Before(g.until) && !now.UTC().Before(g.until.UTC())
}

func (g *ciRateGate) current() (time.Time, bool) {
	now := g.clock()
	if now.IsZero() || now.Before(g.last) || now.UTC().Before(g.last.UTC()) {
		return time.Time{}, false
	}
	g.last = now
	return now, true
}

// observe runs under the single in-flight lock before the response is released.
// See https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api#exceeding-the-rate-limit.
func (g *ciRateGate) observe(status int, h http.Header) bool {
	now, ok := g.current()
	delay, blocked, unsupported := ciRateDirective(now, status, h)
	if !blocked {
		return !ok
	}
	if !ok || unsupported {
		// Never truncate an unrepresentable explicit wait into an earlier
		// request. A replacement reader has no inherited local state.
		g.refused = true
		return true
	}
	until := now.Add(delay)
	if until.After(g.until) {
		g.until = until
	}
	return true
}

func ciRateDirective(now time.Time, status int, h http.Header) (time.Duration, bool, bool) {
	retry := h.Values("Retry-After")
	remaining := h.Values("X-RateLimit-Remaining")
	reset := h.Values("X-RateLimit-Reset")
	limited := status == http.StatusForbidden || status == http.StatusTooManyRequests
	if len(retry) > ciRateMaxHeaderValues || len(remaining) > ciRateMaxHeaderValues {
		return 0, true, true
	}
	var zero, positive, unknown bool
	for _, v := range remaining {
		n, valid, _ := ciRateDecimal(v)
		switch {
		case !valid:
			unknown = true
		case n == 0:
			zero = true
		default:
			positive = true
		}
	}
	unknown = unknown || (zero && positive)
	explicitQuota := zero || unknown
	if !limited && len(retry) == 0 && !explicitQuota {
		return 0, false, false
	}
	var delay time.Duration
	for _, v := range retry {
		n, valid, tooLarge := ciRateDecimal(v)
		if tooLarge || (valid && n > uint64(ciRateMaxDelay/time.Second)) {
			return 0, true, true
		}
		if !valid {
			return 0, true, true
		}
		delay = max(delay, time.Duration(n)*time.Second)
	}
	if explicitQuota {
		if len(reset) > ciRateMaxHeaderValues {
			return 0, true, true
		}
		if len(reset) == 0 {
			// Primary limits have an hourly allowance. With no explicit
			// primary reset at all, do not try the next 5-minute cycle.
			delay = max(delay, ciRateMissingResetFloor)
		}
		for _, v := range reset {
			n, valid, tooLarge := ciRateDecimal(v)
			if tooLarge || !valid || now.Unix() < 0 || n <= uint64(now.Unix()) || n > uint64(now.Add(ciRateMaxDelay).Unix()) {
				// An explicit but unusable reset does not establish any
				// safe later deadline. Do not guess one for this reader.
				return 0, true, true
			}
			delay = max(delay, time.Unix(int64(n), 0).Sub(now))
		}
	}
	if delay == 0 {
		delay = ciRateSecondaryFallback
	}
	return delay, true, false
}

// Non-decimal explicit directives are malformed. Values that exceed the
// bounded interpretation are unsupported, not made into short waits.
func ciRateDecimal(s string) (n uint64, valid, tooLarge bool) {
	if len(s) > 20 {
		return 0, false, true // Avoid scanning attacker-controlled long values.
	}
	if len(s) == 0 {
		return 0, false, false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, false, false
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil, err != nil
}
