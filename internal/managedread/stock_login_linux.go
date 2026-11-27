package managedread

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

var stockJWT = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)

// stockTokenCache is private to the reader. Use the same pinned outbound
// transport and authority as inventory; never pass this to the gateway.
type stockTokenCache struct {
	mu                     sync.Mutex
	token                  string
	expiry, nextAttempt    time.Time
	passwordFile, username string
	request                func(context.Context, []byte) (*http.Response, error)
}

func (c *stockTokenCache) current(ctx context.Context, rejected string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		return "", ErrConfiguration
	}
	now := time.Now()
	if c.token != "" && c.token != rejected && now.Add(time.Minute).Before(c.expiry) {
		return c.token, nil
	}
	if now.Before(c.nextAttempt) {
		return "", ErrConfiguration
	}
	// Bound retry cadence even when credentials are wrong or upstream is down.
	c.nextAttempt = now.Add(5 * time.Second)
	pw, err := securefiles.Read(c.passwordFile, 4096)
	if err != nil {
		return "", ErrConfiguration
	}
	defer clear(pw)
	pass := bytes.TrimSuffix(bytes.TrimSuffix(pw, []byte("\n")), []byte("\r"))
	if len(pass) == 0 || bytes.ContainsAny(pass, "\x00\r\n") {
		return "", ErrConfiguration
	}
	body, err := json.Marshal(struct {
		User     string `json:"username"`
		Password string `json:"password"`
	}{c.username, string(pass)})
	if err != nil {
		return "", ErrConfiguration
	}
	defer clear(body)
	resp, err := c.request(ctx, body)
	if err != nil {
		return "", ErrConfiguration
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", ErrConfiguration
	}
	const maximum = 20 << 10
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maximum+1))
	defer clear(raw)
	if err != nil || len(raw) > maximum {
		return "", ErrConfiguration
	}
	var login struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(raw, &login) != nil || len(login.Token) > 16384 || !stockJWT.MatchString(login.Token) {
		return "", ErrConfiguration
	}
	// JWT claims inform caching only. Stock GARM verifies the signature and
	// privileges on every inventory call, followed by controller identity checks.
	parts := strings.Split(login.Token, ".")
	claim, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ErrConfiguration
	}
	defer clear(claim)
	var jwt struct {
		Expires int64 `json:"exp"`
	}
	if json.Unmarshal(claim, &jwt) != nil || !now.Add(time.Minute).Before(time.Unix(jwt.Expires, 0)) {
		return "", ErrConfiguration
	}
	c.token, c.expiry = login.Token, time.Unix(jwt.Expires, 0)
	c.nextAttempt = time.Time{}
	return c.token, nil
}
