package stockidentity

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
)

// InstanceObservation links a pending stock instance to its observed pool and
// GitHub runner ID. It is current source evidence, not original JIT preparation,
// native ownership, a remote lock, or permission to release resources.
type InstanceFacts struct {
	ID, Name, Pool, Provider, Repository string
	AgentID                              int64
}
type InstanceObservation struct {
	facts  InstanceFacts
	source string
	at     time.Time
	valid  bool
}

func (o InstanceObservation) Valid() bool { return o.valid && !o.at.IsZero() }
func (o InstanceObservation) Facts() InstanceFacts {
	if !o.Valid() {
		return InstanceFacts{}
	}
	return o.facts
}
func (o InstanceObservation) SourceIdentity() string { return o.source }
func ObserveInstance(ctx context.Context, c Configuration, name string) (InstanceObservation, error) {
	fail := func() (InstanceObservation, error) { return InstanceObservation{}, ErrObservation }
	if len(name) == 0 || len(name) > 63 {
		return fail()
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return fail()
		}
	}
	pool, e := Observe(ctx, c)
	if e != nil {
		return fail()
	}
	u, e := url.Parse(c.Endpoint)
	if e != nil {
		return fail()
	}
	u.Path = "/api/v1"
	u.Path += "/instances/" + name
	trust := c.TLS.Clone()
	trust.RootCAs = c.TLS.RootCAs.Clone()
	trust.ServerName = u.Hostname()
	if trust.MinVersion < tls.VersionTLS12 {
		trust.MinVersion = tls.VersionTLS12
	}
	transport := &http.Transport{TLSClientConfig: trust, Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 5 * time.Second, TLSHandshakeTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16384}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrObservation }}
	var managed *managedsource.Client
	if c.ManagedServerKeyPin != "" {
		managed, e = managedsource.New(c.Endpoint, c.ManagedServerKeyPin, c.ManagedControllerID, c.TLS)
		if e != nil {
			return fail()
		}
		defer managed.CloseIdleConnections()
		client.Transport = managed
		u.Path = "/api/v1/instances"
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return fail()
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	res, e := client.Do(req)
	if e != nil {
		return fail()
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fail()
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if e != nil || len(raw) > 4<<20 {
		return fail()
	}
	if managed != nil {
		if managed.Identity() == "" || managed.Identity() != pool.SourceIdentity() {
			return fail()
		}
		var rows []json.RawMessage
		if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '[' || json.Unmarshal(raw, &rows) != nil || len(rows) > 10000 {
			return fail()
		}
		var selected json.RawMessage
		seenNames := map[string]bool{}
		for _, row := range rows {
			fields, err := instanceFields(row)
			if err != nil {
				return fail()
			}
			n, err := text(fields, "name", true)
			if err != nil || seenNames[n] {
				return fail()
			}
			seenNames[n] = true
			if n == name {
				selected = row
			}
		}
		if selected == nil {
			return fail()
		}
		raw = selected
	}
	fields, e := instanceFields(raw)
	if e != nil {
		return fail()
	}
	var v InstanceFacts
	for key, dst := range map[string]*string{"id": &v.ID, "name": &v.Name, "pool_id": &v.Pool, "provider_name": &v.Provider} {
		s, e := text(fields, key, true)
		if e != nil {
			return fail()
		}
		*dst = s
	}
	if json.Unmarshal(fields["agent_id"], &v.AgentID) != nil || v.AgentID <= 0 || !uuid(v.ID) || v.Name != name || v.Pool != c.Pool || v.Provider != c.Provider {
		return fail()
	}
	providerID, e := text(fields, "provider_id", false)
	if e != nil || providerID != "" {
		return fail()
	}
	status, e := text(fields, "status", true)
	if e != nil || (status != "creating" && status != "pending_create") {
		return fail()
	}
	runner, e := text(fields, "runner_status", true)
	if e != nil || runner != "pending" {
		return fail()
	}
	if v, ok := fields["scale_set_id"]; ok {
		var id uint64
		if json.Unmarshal(v, &id) != nil || id != 0 {
			return fail()
		}
	}
	v.Repository = pool.Facts().Repository
	return InstanceObservation{facts: v, source: pool.SourceIdentity(), at: time.Now().UTC(), valid: true}, nil
}

func instanceFields(raw []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, e := d.Token()
	if e != nil || token != json.Delim('{') {
		return nil, ErrObservation
	}
	fields := map[string]json.RawMessage{}
	seen := map[string]bool{}
	for d.More() {
		token, e = d.Token()
		k, ok := token.(string)
		if e != nil || !ok {
			return nil, ErrObservation
		}
		if seen[strings.ToLower(k)] {
			return nil, ErrObservation
		}
		seen[strings.ToLower(k)] = true
		var v json.RawMessage
		if d.Decode(&v) != nil {
			return nil, ErrObservation
		}
		fields[k] = v
	}
	token, e = d.Token()
	if e != nil || token != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		return nil, ErrObservation
	}
	return fields, nil
}
