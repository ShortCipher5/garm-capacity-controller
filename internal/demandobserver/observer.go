// Package demandobserver wires read-only stock observations into a bounded poller.
// It deliberately has no writer, admission, grant or lifecycle capability.
package demandobserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

var ErrObserver = errors.New("read-only demand observer unavailable")

type Config struct {
	Enabled           bool                      `json:"enabled"`
	GarmEndpoint      string                    `json:"garm_endpoint"`
	GarmTokenFile     string                    `json:"garm_token_file"`
	GarmCAFile        string                    `json:"garm_ca_file"`
	GitHubEndpoint    string                    `json:"github_endpoint"`
	GitHubAuthority   string                    `json:"github_authority"`
	GitHubTokenFile   string                    `json:"github_token_file"`
	GitHubCAFile      string                    `json:"github_ca_file"`
	Providers         []string                  `json:"providers"`
	Repositories      []demandrepair.Repository `json:"repositories"`
	CheckpointPath    string                    `json:"checkpoint_path"`
	InitialFrom       time.Time                 `json:"initial_from"`
	IntervalSeconds   int                       `json:"interval_seconds"`
	MaxBackoffSeconds int                       `json:"max_backoff_seconds"`
}

type Counts struct {
	StockQueued       int   `json:"stock_queued"`
	ObservedDemand    int   `json:"observed_demand"`
	Conflicted        int   `json:"conflicted"`
	MissingFromReader int   `json:"missing_from_reader"`
	Pools             int   `json:"pools"`
	Idle              int64 `json:"idle"`
	Creating          int64 `json:"creating"`
	Busy              int64 `json:"busy"`
	Unknown           int64 `json:"unknown"`
}
type Event struct {
	Mode                  string     `json:"mode"`
	Status                string     `json:"status"`
	StartedAt             time.Time  `json:"started_at"`
	FinishedAt            time.Time  `json:"finished_at"`
	LastSuccessAt         *time.Time `json:"last_success_at,omitempty"`
	Counts                *Counts    `json:"counts,omitempty"`
	Atomic                bool       `json:"atomic"`
	MissedWebhookCoverage bool       `json:"missed_webhook_coverage"`
	NextPollSeconds       int        `json:"next_poll_seconds"`
}

// Load accepts only a private owner-readable file and rejects unknown options.
// Neither errors nor emitted events include configuration or upstream content.
func Load(path string) (Config, error) {
	data, e := securefiles.Read(path, 1<<20)
	if e != nil {
		return Config{}, ErrObserver
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var c Config
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.validate() != nil {
		return Config{}, ErrObserver
	}
	return c, nil
}
func (c Config) validate() error {
	if !c.Enabled || c.IntervalSeconds < 60 || c.IntervalSeconds > 86400 || c.MaxBackoffSeconds < c.IntervalSeconds || c.MaxBackoffSeconds > 86400 || c.CheckpointPath == "" || c.InitialFrom.IsZero() || !c.InitialFrom.Before(time.Now()) || len(c.Repositories) == 0 || len(c.Repositories) > 100 {
		return ErrObserver
	}
	return nil
}
func trust(path string) (*tls.Config, error) {
	data, e := securefiles.Read(path, 1<<20)
	if e != nil {
		return nil, ErrObserver
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		return nil, ErrObserver
	}
	return &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, nil
}
func token(path string) (string, error) {
	data, e := securefiles.Read(path, 16384)
	if e != nil {
		return "", ErrObserver
	}
	s := strings.TrimSpace(string(data))
	if s == "" || strings.ContainsAny(s, "\r\n\x00") {
		return "", ErrObserver
	}
	return s, nil
}

// Observe refreshes file-backed tokens each poll and uses verified TLS with no
// redirect/proxy handling from the underlying read-only clients. All network
// calls and checkpoint work share one cancellation/90-second deadline.
func Observe(ctx context.Context, c Config) Event {
	event, _ := ObserveSnapshot(ctx, c)
	return event
}

// Snapshot preserves the full scoped source observations for an in-process
// planner. It is deliberately not included in Event or the CLI summary stream.
// Stock and Repair remain distinct: repaired jobs have no proven stock pool
// assignment, conflicts are not silently resolved, and neither source grants
// physical capacity or proves completion.
type Snapshot struct {
	Stock  garmapi.DemandObservation
	Repair demandrepair.Result
}

// ObserveSnapshot performs the same real transport/checkpoint poll as Observe.
// A failed or cancelled poll returns nil, never partial or previously cached
// demand. Successful empty demand returns a non-nil snapshot. Callers must
// enforce freshness and retain all source provenance before planning.
func ObserveSnapshot(ctx context.Context, c Config) (Event, *Snapshot) {
	var snapshot *Snapshot
	event := Event{Mode: "observation_only", Status: "configuration_unavailable", StartedAt: time.Now().UTC()}
	finish := func() (Event, *Snapshot) { event.FinishedAt = time.Now().UTC(); return event, snapshot }
	if ctx == nil || ctx.Err() != nil || c.validate() != nil {
		return finish()
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	gt, e := trust(c.GarmCAFile)
	if e != nil {
		return finish()
	}
	ht, e := trust(c.GitHubCAFile)
	if e != nil {
		return finish()
	}
	secret, e := token(c.GarmTokenFile)
	if e != nil {
		return finish()
	}
	ids := make([]string, 0, len(c.Repositories))
	for _, r := range c.Repositories {
		ids = append(ids, r.GarmID)
	}
	g, e := garmapi.NewDemand(c.GarmEndpoint, secret, gt, c.Providers, ids)
	if e != nil {
		return finish()
	}
	defer g.Close()
	binding, _ := json.Marshal(struct {
		Endpoint  string
		Providers []string
	}{c.GarmEndpoint, c.Providers})
	scope := sha256.Sum256(binding)
	h, e := demandrepair.NewGitHub(c.GitHubEndpoint, c.GitHubAuthority, "observer-"+hex.EncodeToString(scope[:]), func(ctx context.Context, _ string) (string, error) {
		if ctx.Err() != nil {
			return "", ErrObserver
		}
		return token(c.GitHubTokenFile)
	}, ht, c.Repositories)
	if e != nil {
		return finish()
	}
	defer h.Close()
	// Validate both clients before starting any remote request.
	event.Status = "garm_unavailable"
	stock, e := g.Observe(ctx)
	if e != nil {
		return finish()
	}
	baseline := make([]demandrepair.Baseline, 0, len(stock.Jobs))
	for _, j := range stock.Jobs {
		baseline = append(baseline, demandrepair.Baseline{RepositoryID: j.RepositoryID, Job: demandrepair.Job{ID: j.WorkflowJobID, Status: "queued", Labels: j.Labels}})
	}
	event.Status = "github_or_checkpoint_unavailable"
	repaired, e := demandrepair.PollGitHub(ctx, h, c.Repositories, baseline, demandrepair.PollOptions{Path: c.CheckpointPath, InitialFrom: c.InitialFrom, Overlap: time.Minute, MinInterval: time.Duration(c.IntervalSeconds) * time.Second})
	if e != nil {
		return finish()
	}
	if ctx.Err() != nil {
		return finish()
	}
	counts := &Counts{StockQueued: len(stock.Jobs), ObservedDemand: len(repaired.Items), Pools: len(stock.Pools)}
	for _, item := range repaired.Items {
		if item.Conflict {
			counts.Conflicted++
		}
		if item.MissingFromReader {
			counts.MissingFromReader++
		}
	}
	for _, s := range stock.Supply {
		counts.Idle += s.Idle
		counts.Creating += s.Creating
		counts.Busy += s.Busy
		counts.Unknown += s.Other
	}
	event.Status = "observed"
	event.Counts = counts
	snapshot = &Snapshot{Stock: stock, Repair: repaired}
	return finish()
}

// Run emits newline-delimited summaries only. A failed poll never republishes old
// counts as fresh. Cancellation interrupts both network reads and retry delays.
// once returns an error for an unsuccessful observation after emitting its status.
func Run(ctx context.Context, c Config, out io.Writer, once bool) error {
	if ctx == nil || ctx.Err() != nil || out == nil || c.validate() != nil {
		return ErrObserver
	}
	return run(ctx, c, out, once, Observe, wait)
}
func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func run(ctx context.Context, c Config, out io.Writer, once bool, poll func(context.Context, Config) Event, sleep func(context.Context, time.Duration) error) error {
	encoder := json.NewEncoder(out)
	backoff := c.IntervalSeconds
	var last *time.Time
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		event := poll(ctx, c)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ok := event.Status == "observed"
		if ok {
			v := event.FinishedAt
			last = &v
			backoff = c.IntervalSeconds
		} else {
			event.Counts = nil
		}
		event.LastSuccessAt = last
		if !once {
			event.NextPollSeconds = backoff
		}
		if encoder.Encode(event) != nil {
			return ErrObserver
		}
		if once {
			if !ok {
				return ErrObserver
			}
			return nil
		}
		if err := sleep(ctx, time.Duration(backoff)*time.Second); err != nil {
			return err
		}
		if !ok {
			backoff *= 2
			if backoff > c.MaxBackoffSeconds {
				backoff = c.MaxBackoffSeconds
			}
		}
	}
}
