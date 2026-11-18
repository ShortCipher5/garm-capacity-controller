package managedsettings

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"net/http"
	"time"
	"unicode"
	"unicode/utf8"
)

// HistoryEvent is only a sanitized historical settings receipt. It cannot clear
// a client uncertainty latch or a supervisor's durable pending intent.
type HistoryEvent struct {
	RequestID string               `json:"request_id"`
	Kind      string               `json:"kind"`
	Target    string               `json:"target"`
	State     string               `json:"state"`
	Reason    string               `json:"reason"`
	CreatedAt string               `json:"created_at"`
	Before    garmapi.ManagedPool  `json:"before"`
	Desired   garmapi.ManagedPool  `json:"desired"`
	Result    *garmapi.ManagedPool `json:"result,omitempty"`
}
type History struct {
	Events    []HistoryEvent `json:"events"`
	Truncated bool           `json:"truncated"`
}

func ValidHistoryEvent(e HistoryEvent) bool {
	if !uuid(e.RequestID) || e.Kind != "pool" || !uuid(e.Target) || len(e.Reason) > 256 || !utf8.ValidString(e.Reason) || e.State != "confirmed" && e.State != "pending" && e.State != "unknown" && e.State != "rejected" {
		return false
	}
	for _, r := range e.Reason {
		if unicode.IsControl(r) {
			return false
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, e.CreatedAt); err != nil {
		return false
	}
	for _, p := range []*garmapi.ManagedPool{&e.Before, &e.Desired, e.Result} {
		if p == nil {
			continue
		}
		if p.ID != e.Target || p.ProviderName == "" || len(p.ProviderName) > 256 || !digest(p.Fingerprint) || p.MaxRunners > 1000000 || p.MinIdleRunners > p.MaxRunners || p.Priority > 1000000 || len(p.Tags) > 100 {
			return false
		}
		for _, t := range p.Tags {
			if len(t) > 256 || !utf8.ValidString(t) {
				return false
			}
		}
	}
	return e.State != "confirmed" || e.Result != nil
}
func (c *Client) History(ctx context.Context) (History, error) {
	if e := c.enter(ctx); e != nil {
		return History{}, e
	}
	defer c.leave()
	var h History
	if e := c.exchange(ctx, http.MethodGet, "/v1/managed/history", nil, &h); e != nil {
		return History{}, e
	}
	if h.Events == nil || len(h.Events) > 25 {
		return History{}, ErrSettings
	}
	seen := map[string]bool{}
	out := History{Events: []HistoryEvent{}, Truncated: h.Truncated}
	for _, e := range h.Events {
		if !ValidHistoryEvent(e) || seen[e.RequestID] {
			return History{}, ErrSettings
		}
		seen[e.RequestID] = true
		if !c.pools[e.Target] {
			out.Truncated = true
			continue
		}
		for _, p := range []*garmapi.ManagedPool{&e.Before, &e.Desired, e.Result} {
			if p != nil && !c.providers[p.ProviderName] {
				return History{}, ErrSettings
			}
		}
		out.Events = append(out.Events, e)
	}
	// Extra projection protects callers from any future wire-only fields.
	raw, e := json.Marshal(out)
	if e != nil || len(raw) > 512<<10 {
		return History{}, ErrSettings
	}
	return out, nil
}
