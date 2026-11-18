package managedsource

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// ReadCredential is a separate narrowly scoped read protocol, not a general
// POST transport. Only the instance JWT enters the fixed supervisor endpoint;
// administrative credentials and caller-controlled URLs are never accepted.
func (c *Client) ReadCredential(ctx context.Context, pool, name, file, token string) ([]byte, error) {
	if c == nil || ctx == nil || ctx.Err() != nil || !uuid(pool) || len(name) == 0 || len(name) > 63 || len(token) == 0 || len(token) > 16384 || strings.IndexFunc(token, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return nil, ErrSource
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return nil, ErrSource
		}
	}
	if file != "runner" && file != "credentials" && file != "credentials_rsaparams" {
		return nil, ErrSource
	}
	c.mu.Lock()
	blocked := c.revoked
	c.mu.Unlock()
	if blocked {
		return nil, ErrSource
	}
	raw, e := json.Marshal(struct {
		InstanceToken string `json:"instance_token"`
	}{token})
	if e != nil || len(raw) > 17*1024 {
		return nil, ErrSource
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/managed/metadata/"+pool+"/"+name+"/"+file, bytes.NewReader(raw))
	if e != nil {
		return nil, ErrSource
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/octet-stream")
	client := &http.Client{Transport: c.transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrSource }}
	res, e := client.Do(req)
	if e != nil {
		return nil, ErrSource
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || c.verifySource(res) != nil {
		return nil, ErrSource
	}
	out, e := io.ReadAll(io.LimitReader(res.Body, (1<<19)+1))
	if e != nil || len(out) == 0 || len(out) > 1<<19 {
		return nil, ErrSource
	}
	if c.Identity() == "" {
		return nil, ErrSource
	}
	return out, nil
}
