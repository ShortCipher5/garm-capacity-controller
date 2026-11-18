//go:build linux

// Package nativeids permanently consumes subordinate ID ranges for fresh native
// namespace births. A reservation is NOT evidence that a namespace was created,
// storage delegated, or physical capacity admitted. There is no release API.
package nativeids

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var ErrReservation = errors.New("native subordinate ID reservation refused; never reuse uncertain ranges")

const Account = "garm-capacity-native"
const BlockSize uint64 = 1 << 24

type interval struct{ start, count uint64 }
type Record struct {
	Version    int       `json:"version"`
	BirthID    string    `json:"birth_id"`
	UIDStart   uint64    `json:"uid_start"`
	GIDStart   uint64    `json:"gid_start"`
	Count      uint64    `json:"count"`
	ConsumedAt time.Time `json:"consumed_at"`
}

// Reservation is minted only after a root-owned record and its directory have
// been fsynced. It grants no process, host or ledger authority.
type Reservation struct {
	record  Record
	claimed *atomic.Bool
}

func (r Reservation) Record() Record { return r.record }

// Claim permits one construction attempt from this non-reconstructible handle.
// Copies share the same consumed bit. Lost handles/failed attempts cannot be
// reloaded from the record, and no future allocator can reuse the recorded IDs.
func (r Reservation) Claim() (Record, error) {
	if !valid(r.record) || r.claimed == nil || !r.claimed.CompareAndSwap(false, true) {
		return Record{}, ErrReservation
	}
	return r.record, nil
}

// Reserve uses only the dedicated OS subordinate-ID assignments, rejecting any
// overlaps with other accounts. Registry must already be a dedicated root0700
// directory. Every attempt permanently consumes a block before any child launch;
// incomplete/malformed records poison the registry rather than being reclaimed.
func Reserve(registry string) (Reservation, error) {
	if os.Geteuid() != 0 || securePath(registry, true) != nil {
		return Reservation{}, ErrReservation
	}
	st, e := os.Stat(registry)
	if e != nil || st.Mode().Perm() != 0700 {
		return Reservation{}, ErrReservation
	}
	uidRaw, e := readRoot("/etc/subuid", 1<<20)
	if e != nil {
		return Reservation{}, e
	}
	gidRaw, e := readRoot("/etc/subgid", 1<<20)
	if e != nil {
		return Reservation{}, e
	}
	u, e := assignments(uidRaw)
	if e != nil {
		return Reservation{}, e
	}
	g, e := assignments(gidRaw)
	if e != nil {
		return Reservation{}, e
	}
	passwd, e := readRoot("/etc/passwd", 1<<20)
	if e != nil || rejectLocalIDs(passwd, u, 7, 2) != nil {
		return Reservation{}, ErrReservation
	}
	if rejectLocalIDs(passwd, g, 7, 3) != nil {
		return Reservation{}, ErrReservation
	}
	groups, e := readRoot("/etc/group", 1<<20)
	if e != nil || rejectLocalIDs(groups, g, 4, 2) != nil {
		return Reservation{}, ErrReservation
	}
	lock, e := unix.Open(filepath.Join(registry, ".lock"), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if e != nil {
		return Reservation{}, ErrReservation
	}
	defer unix.Close(lock)
	var lst unix.Stat_t
	if unix.Fstat(lock, &lst) != nil || lst.Uid != 0 || lst.Mode&unix.S_IFMT != unix.S_IFREG || lst.Mode&0777 != 0600 || unix.Flock(lock, unix.LOCK_EX) != nil {
		return Reservation{}, ErrReservation
	}
	defer unix.Flock(lock, unix.LOCK_UN)
	entries, e := os.ReadDir(registry)
	if e != nil {
		return Reservation{}, ErrReservation
	}
	records := make([]Record, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == ".lock" {
			continue
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".json") {
			return Reservation{}, ErrReservation
		}
		raw, e := readRoot(filepath.Join(registry, entry.Name()), 4096)
		if e != nil {
			return Reservation{}, e
		}
		var r Record
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if dec.Decode(&r) != nil || dec.Decode(new(any)) != io.EOF || !valid(r) || entry.Name() != r.BirthID+".json" {
			return Reservation{}, ErrReservation
		}
		canonical, _ := json.Marshal(r)
		if !bytes.Equal(canonical, raw) || !contains(u, interval{r.UIDStart, r.Count}) || !contains(g, interval{r.GIDStart, r.Count}) {
			return Reservation{}, ErrReservation
		}
		for _, old := range records {
			if overlap(interval{r.UIDStart, r.Count}, interval{old.UIDStart, old.Count}) || overlap(interval{r.GIDStart, r.Count}, interval{old.GIDStart, old.Count}) {
				return Reservation{}, ErrReservation
			}
		}
		records = append(records, r)
	}
	uid, e := next(u, records, true)
	if e != nil {
		return Reservation{}, e
	}
	gid, e := next(g, records, false)
	if e != nil {
		return Reservation{}, e
	}
	var id [32]byte
	if _, e = rand.Read(id[:]); e != nil {
		return Reservation{}, ErrReservation
	}
	r := Record{1, hex.EncodeToString(id[:]), uid, gid, BlockSize, time.Now().UTC()}
	raw, _ := json.Marshal(r)
	f, e := os.OpenFile(filepath.Join(registry, r.BirthID+".json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return Reservation{}, ErrReservation
	}
	// Do not remove the record on any error: its existence consumes this attempt.
	_, e = f.Write(raw)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil || ce != nil {
		return Reservation{}, ErrReservation
	}
	d, e := os.Open(registry)
	if e != nil {
		return Reservation{}, ErrReservation
	}
	e = d.Sync()
	d.Close()
	if e != nil {
		return Reservation{}, ErrReservation
	}
	return Reservation{record: r, claimed: new(atomic.Bool)}, nil
}
func valid(r Record) bool {
	b, e := hex.DecodeString(r.BirthID)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == r.BirthID && r.Version == 1 && r.Count == BlockSize && r.UIDStart > 0 && r.GIDStart > 0 && r.UIDStart <= 1<<32-1 && r.GIDStart <= 1<<32-1 && r.UIDStart+r.Count <= 1<<32-1 && r.GIDStart+r.Count <= 1<<32-1 && !r.ConsumedAt.IsZero()
}
func overlap(a, b interval) bool  { return a.start < b.start+b.count && b.start < a.start+a.count }
func contains(a, b interval) bool { return b.start >= a.start && b.start+b.count <= a.start+a.count }
func assignments(raw []byte) (interval, error) {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return interval{}, ErrReservation
	}
	var own interval
	all := []interval{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) != 3 || fields[0] == "" {
			return interval{}, ErrReservation
		}
		start, e := strconv.ParseUint(fields[1], 10, 32)
		if e != nil || strconv.FormatUint(start, 10) != fields[1] {
			return interval{}, ErrReservation
		}
		count, e := strconv.ParseUint(fields[2], 10, 32)
		if e != nil || strconv.FormatUint(count, 10) != fields[2] || start == 0 || count == 0 || start+count > 1<<32-1 {
			return interval{}, ErrReservation
		}
		v := interval{start, count}
		for _, old := range all {
			if overlap(v, old) {
				return interval{}, ErrReservation
			}
		}
		all = append(all, v)
		if fields[0] == Account {
			if own.count != 0 {
				return interval{}, ErrReservation
			}
			own = v
		}
	}
	if own.count < BlockSize {
		return interval{}, ErrReservation
	}
	return own, nil
}
func next(pool interval, records []Record, uid bool) (uint64, error) {
	for start := pool.start; start+BlockSize <= pool.start+pool.count; start += BlockSize {
		candidate := interval{start, BlockSize}
		free := true
		for _, r := range records {
			other := r.GIDStart
			if uid {
				other = r.UIDStart
			}
			if overlap(candidate, interval{other, r.Count}) {
				free = false
				break
			}
		}
		if free {
			return start, nil
		}
	}
	return 0, ErrReservation
}
func securePath(path string, dir bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrReservation
	}
	current := "/"
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		current = filepath.Join(current, part)
		st, e := os.Lstat(current)
		if e != nil || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0022 != 0 {
			return ErrReservation
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok || sys.Uid != 0 {
			return ErrReservation
		}
		if current != path || dir {
			if !st.IsDir() {
				return ErrReservation
			}
		}
	}
	return nil
}
func readRoot(path string, max int64) ([]byte, error) {
	if securePath(path, false) != nil {
		return nil, ErrReservation
	}
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, ErrReservation
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() {
		return nil, ErrReservation
	}
	raw, e := io.ReadAll(io.LimitReader(f, max+1))
	if e != nil || int64(len(raw)) > max {
		return nil, fmt.Errorf("%w: unreadable bounded record", ErrReservation)
	}
	return raw, nil
}

// Local account IDs must not occupy the namespace-only assignment. External
// identity provisioning must also reserve this dedicated OS range; this code
// does not pretend /etc files enumerate LDAP or future root-created accounts.
func rejectLocalIDs(raw []byte, pool interval, fields, index int) error {
	if len(raw) == 0 || len(raw) > 1<<20 || index < 0 || index >= fields {
		return ErrReservation
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) != fields || parts[0] == "" {
			return ErrReservation
		}
		id, e := strconv.ParseUint(parts[index], 10, 32)
		if e != nil || strconv.FormatUint(id, 10) != parts[index] || (id >= pool.start && id < pool.start+pool.count) {
			return ErrReservation
		}
	}
	return nil
}
