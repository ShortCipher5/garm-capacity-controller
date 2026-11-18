package managedsource

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

const SourcePath = "/v1/managed/source"
const NonceHeader = "X-Capacity-Source-Nonce"

// Observation is minted only by a concrete authenticated descriptor read. It
// links the managed stock origin, not native or capacity activation authority.
type Observation struct {
	identity, controller, stockSource string
	at                                time.Time
}

func (o Observation) SourceIdentity() string      { return o.identity }
func (o Observation) StockSourceIdentity() string { return o.stockSource }
func (o Observation) ControllerID() string        { return o.controller }
func (o Observation) ObservedAt() time.Time       { return o.at }
func (o Observation) Valid() bool {
	return o.identity != "" && o.controller != "" && o.stockSource != "" && !o.at.IsZero() && time.Since(o.at) <= 5*time.Second && !o.at.After(time.Now().Add(time.Second))
}
func (c *Client) ObserveSource(ctx context.Context) (Observation, error) {
	if c == nil || ctx == nil || ctx.Err() != nil {
		return Observation{}, ErrSource
	}
	var nonce [32]byte
	if _, e := rand.Read(nonce[:]); e != nil {
		return Observation{}, ErrSource
	}
	n := hex.EncodeToString(nonce[:])
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, c.base+SourcePath, nil)
	if e != nil {
		return Observation{}, ErrSource
	}
	req.Header.Set(NonceHeader, n)
	h := &http.Client{Transport: c, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrSource }}
	res, e := h.Do(req)
	if e != nil {
		return Observation{}, ErrSource
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return Observation{}, ErrSource
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, 2049))
	if e != nil || len(raw) > 2048 {
		return Observation{}, ErrSource
	}
	var v struct {
		Version    string    `json:"version"`
		Nonce      string    `json:"nonce"`
		Controller string    `json:"controller"`
		Source     string    `json:"source"`
		ObservedAt time.Time `json:"observed_at"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Version != "managed-source/v1" || v.Nonce != n || v.Controller != c.controller || v.Source != res.Header.Get(SourceHeader) {
		return Observation{}, ErrSource
	}
	canonical, e := json.Marshal(v)
	if e != nil || string(raw) != string(canonical) {
		return Observation{}, ErrSource
	}
	o := Observation{c.Identity(), v.Controller, v.Source, v.ObservedAt}
	if !o.Valid() {
		return Observation{}, ErrSource
	}
	return o, nil
}
