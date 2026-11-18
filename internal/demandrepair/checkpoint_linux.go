package demandrepair

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// PollCheckpoint is observation history only, never admission/release authority.
// Missing history starts at InitialFrom and rescans active backlog, not at now.
type PollOptions struct {
	Path                 string
	InitialFrom          time.Time
	Overlap, MinInterval time.Duration
}
type checkpoint struct {
	Version     int
	Binding     string
	Through     time.Time
	LookupAfter string
	Result      Result
}

// PollGitHub serializes one scoped poll and atomically checkpoints only a complete
// successful observation. Call periodically outside this package; no daemon,
// credential configuration or live runtime activation is installed here.
func PollGitHub(ctx context.Context, c *GitHubClient, repos []Repository, stock []Baseline, options PollOptions) (Result, error) {
	if c == nil || len(stock) > 10000 {
		return Result{}, ErrObservation
	}
	raw, e := json.Marshal(struct {
		Endpoint, Reference string
		Repositories        []Repository
		InitialFrom         time.Time
		Overlap             time.Duration
	}{c.base, c.ref, repos, options.InitialFrom, options.Overlap})
	if e != nil {
		return Result{}, ErrObservation
	}
	sum := sha256.Sum256(raw)
	return poll(ctx, hex.EncodeToString(sum[:]), options, func(ctx context.Context, history []Item, from, to time.Time, cursor *string) (Result, error) {
		baseline := []Baseline{}
		seen := map[string]Job{}
		past := map[string]Item{}
		current := map[string]bool{}
		for _, item := range history {
			k := key(item.RepositoryID, item.Job.ID)
			past[k] = item
			seen[k] = item.Job
			baseline = append(baseline, Baseline{RepositoryID: item.RepositoryID, Job: item.Job})
		}
		for _, b := range stock {
			if !validJob(b.Job) || b.Job.Status != "queued" {
				return Result{}, ErrObservation
			}
			k := key(b.RepositoryID, b.Job.ID)
			if current[k] {
				return Result{}, ErrObservation
			}
			current[k] = true
			if old, ok := seen[k]; ok {
				if !equalLabels(canonical(old).Labels, canonical(b.Job).Labels) {
					return Result{}, ErrObservation
				}
				continue
			}
			seen[k] = b.Job
			baseline = append(baseline, b)
		}
		result, err := ObserveGitHub(ctx, c, repos, baseline, from, to)
		if err != nil {
			return Result{}, err
		}
		// Older completed runs may be outside both the recent window and the
		// active-run search. Query a bounded rotating subset of missing history.
		// Failed/ambiguous lookups remain unknown and are never deleted.
		*cursor = lookupMissingHistory(ctx, c, repos, result.Items, past, current, *cursor)
		retainedItems := result.Items[:0]
		for i := range result.Items {
			item := &result.Items[i]
			k := key(item.RepositoryID, item.Job.ID)
			previous, retained := past[k]
			// Only remove historical demand after a fresh positive completed
			// observation and no current stock queue claim. Missing rows, old
			// checkpoint status and in-progress jobs do not qualify. This only
			// forgets an advisory queue item; no resource charge is released.
			if retained && !current[k] && !item.MissingFromReader && !item.LabelsConflict && item.ReaderStatus == "completed" {
				continue
			}
			if current[k] || !retained {
				retainedItems = append(retainedItems, *item)
				continue
			}
			item.PriorSource = previous.PriorSource
			if item.PriorSource == "" {
				item.PriorSource = previous.Source
			}
			if item.MissingFromReader {
				item.Source = "history"
				item.Conflict = item.Conflict || previous.Conflict
			} else {
				item.Source = "reader"
			}
			retainedItems = append(retainedItems, *item)
		}
		result.Items = retainedItems
		return result, nil
	})
}

// lookupMissingHistory never changes physical accounting. It rotates across
// missing entries to avoid permanently starving old jobs behind a 32-request cap.
func lookupMissingHistory(ctx context.Context, c *GitHubClient, repos []Repository, items []Item, past map[string]Item, current map[string]bool, after string) string {
	if ctx == nil || c == nil {
		return after
	}
	byID := map[string]Repository{}
	for _, r := range repos {
		byID[r.GarmID] = r
	}
	candidates := []int{}
	for i, item := range items {
		k := key(item.RepositoryID, item.Job.ID)
		if _, ok := past[k]; ok && !current[k] && item.MissingFromReader {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := items[candidates[i]], items[candidates[j]]
		return key(a.RepositoryID, a.Job.ID) < key(b.RepositoryID, b.Job.ID)
	})
	lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := 0
	for n, i := range candidates {
		if key(items[i].RepositoryID, items[i].Job.ID) > after {
			start = n
			break
		}
	}
	for n := 0; n < len(candidates) && n < 32; n++ {
		if lookupCtx.Err() != nil {
			return after
		}
		i := candidates[(start+n)%len(candidates)]
		after = key(items[i].RepositoryID, items[i].Job.ID)
		repo, ok := byID[items[i].RepositoryID]
		if !ok {
			continue
		}
		job, err := c.lookupJob(lookupCtx, repo, items[i].Job.ID)
		if err != nil {
			continue
		}
		// A label mismatch is an identity/conflict warning, not a completion.
		if !equalLabels(canonical(items[i].Job).Labels, job.Labels) {
			items[i].Conflict = true
			items[i].LabelsConflict = true
			continue
		}
		items[i].MissingFromReader = false
		items[i].ReaderStatus = job.Status
		if job.Status != "queued" {
			items[i].Conflict = true
		}
	}
	return after
}

func privateFile(path string, flags int) (*os.File, error) {
	fd, e := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return nil, ErrObservation
	}
	f := os.NewFile(uintptr(fd), path)
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		f.Close()
		return nil, ErrObservation
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		f.Close()
		return nil, ErrObservation
	}
	return f, nil
}
func poll(ctx context.Context, binding string, o PollOptions, observe func(context.Context, []Item, time.Time, time.Time, *string) (Result, error)) (Result, error) {
	now := time.Now().UTC()
	if ctx == nil || ctx.Err() != nil || binding == "" || o.Path == "" || o.InitialFrom.IsZero() || !o.InitialFrom.Before(now) || o.Overlap < time.Second || o.Overlap > 24*time.Hour || o.MinInterval < time.Minute || o.MinInterval > 24*time.Hour {
		return Result{}, ErrObservation
	}
	path, e := filepath.Abs(o.Path)
	if e != nil {
		return Result{}, ErrObservation
	}
	directory := filepath.Dir(path)
	info, e := os.Lstat(directory)
	if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return Result{}, ErrObservation
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return Result{}, ErrObservation
	}
	lock, e := privateFile(path+".lock", syscall.O_CREAT|syscall.O_RDWR)
	if e != nil {
		return Result{}, e
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return Result{}, ErrObservation
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	from := o.InitialFrom
	history := []Item{}
	cursor := ""
	if _, e = os.Lstat(path); e == nil {
		f, e := privateFile(path, syscall.O_RDONLY)
		if e != nil {
			return Result{}, e
		}
		data, e := io.ReadAll(io.LimitReader(f, (8<<20)+1))
		f.Close()
		var saved checkpoint
		if e != nil || len(data) > 8<<20 || json.Unmarshal(data, &saved) != nil || saved.Version != 1 || saved.Binding != binding || saved.Through.IsZero() || saved.Through.After(now) || !saved.Result.EnumerationComplete || len(saved.Result.Items) > 10000 {
			return Result{}, ErrObservation
		}
		if now.Sub(saved.Through) < o.MinInterval {
			return Result{}, ErrObservation
		}
		if len(saved.LookupAfter) > 512 {
			return Result{}, ErrObservation
		}
		cursor = saved.LookupAfter
		from = saved.Through.Add(-o.Overlap)
		if from.Before(o.InitialFrom) {
			from = o.InitialFrom
		}
		seen := map[string]bool{}
		for _, item := range saved.Result.Items {
			if item.RepositoryID == "" || !validJob(item.Job) || item.Job.Status != "queued" {
				return Result{}, ErrObservation
			}
			k := key(item.RepositoryID, item.Job.ID)
			if seen[k] {
				return Result{}, ErrObservation
			}
			seen[k] = true
			history = append(history, item)
		}
	} else if !os.IsNotExist(e) {
		return Result{}, ErrObservation
	}
	result, e := observe(ctx, history, from, now, &cursor)
	if e != nil || ctx.Err() != nil || !result.EnumerationComplete {
		return Result{}, ErrObservation
	}
	data, e := json.Marshal(checkpoint{Version: 1, Binding: binding, Through: now, LookupAfter: cursor, Result: result})
	if e != nil || len(data) > 8<<20 {
		return Result{}, ErrObservation
	}
	temp, e := os.CreateTemp(directory, ".demand-checkpoint-*")
	if e != nil {
		return Result{}, ErrObservation
	}
	defer os.Remove(temp.Name())
	if _, e = temp.Write(data); e != nil {
		temp.Close()
		return Result{}, ErrObservation
	}
	if e = temp.Sync(); e != nil {
		temp.Close()
		return Result{}, ErrObservation
	}
	if e = temp.Close(); e != nil {
		return Result{}, ErrObservation
	}
	if e = os.Rename(temp.Name(), path); e != nil {
		return Result{}, ErrObservation
	}
	dir, e := os.Open(directory)
	if e != nil {
		return Result{}, ErrObservation
	}
	e = dir.Sync()
	dir.Close()
	if e != nil {
		return Result{}, ErrObservation
	}
	return result, nil
}
