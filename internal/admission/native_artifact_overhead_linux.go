package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeartifacts"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrArtifactOverhead = errors.New("canonical host artifact overhead unavailable; retain all charges")

const sqliteNativeArtifactOverheadSchema = `
CREATE TABLE native_artifact_overheads(record_sequence INTEGER PRIMARY KEY NOT NULL REFERENCES audit(sequence),root_key TEXT NOT NULL,host TEXT NOT NULL,payload TEXT NOT NULL CHECK(length(CAST(payload AS BLOB)) BETWEEN 1 AND 16384),payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64)) STRICT;
CREATE TRIGGER native_artifact_overhead_immutable BEFORE UPDATE ON native_artifact_overheads BEGIN SELECT RAISE(ABORT,'artifact overhead immutable'); END;
CREATE TRIGGER native_artifact_overhead_permanent BEFORE DELETE ON native_artifact_overheads BEGIN SELECT RAISE(ABORT,'artifact overhead permanent'); END;
DROP TRIGGER reservation_current_accounting;
CREATE TRIGGER reservation_current_accounting BEFORE INSERT ON reservations WHEN NEW.accounting_version!=27 BEGIN SELECT RAISE(ABORT,'current artifact overhead accounting required'); END;
DROP TRIGGER dispatch_current_accounting;
CREATE TRIGGER dispatch_current_accounting BEFORE INSERT ON dispatches WHEN NEW.grant_accounting_version!=27 BEGIN SELECT RAISE(ABORT,'current artifact overhead accounting required'); END;
PRAGMA user_version=27;
`

func artifactGUID(v string) bool {
	n, e := strconv.ParseUint(v, 10, 64)
	return e == nil && n > 0 && strconv.FormatUint(n, 10) == v
}
func artifactRootKey(v NativeArtifactOverhead) string { return v.PoolGUID + ":" + v.RootGUID }
func artifactDataset(v string) bool {
	if len(v) == 0 || len(v) > 512 || !strings.Contains(v, "/") {
		return false
	}
	for _, part := range strings.Split(v, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.:-", r)) {
				return false
			}
		}
	}
	return true
}

// ValidateNativeArtifactOverhead validates a detached historical projection,
// never authentication, mutation permission, or physical release evidence.
func ValidateNativeArtifactOverhead(v NativeArtifactOverhead) error {
	if nativeartifacts.ValidateDescriptor(nativeartifacts.Descriptor{Version: nativeartifacts.Version, Nonce: v.Nonce, IssuedAt: v.IssuedAt, Owner: v.Owner, ArtifactID: v.ArtifactID, PoolGUID: v.PoolGUID, PoolLoadGUID: v.PoolLoadGUID, RootGUID: v.RootGUID, Dataset: v.Dataset, QuotaBytes: int64(v.QuotaBytes)}) != nil {
		return ErrArtifactOverhead
	}

	if v.Sequence <= 0 || v.DomainBindingSequence <= 0 || v.DomainBindingSequence >= v.Sequence || !bindingName(v.Host) || !bindingFingerprint(v.ArtifactID) || !bindingFingerprint(v.Nonce) || !artifactGUID(v.PoolGUID) || !artifactGUID(v.PoolLoadGUID) || !artifactGUID(v.RootGUID) || !artifactDataset(v.Dataset) || v.QuotaBytes == 0 || v.QuotaBytes > math.MaxInt64 || v.RecordedAt.IsZero() || v.ObservedAt.IsZero() || v.IssuedAt.IsZero() || v.ObservedAt.After(v.RecordedAt.Add(time.Second)) || v.RecordedAt.Sub(v.ObservedAt) > 5*time.Second || v.IssuedAt.After(v.RecordedAt.Add(time.Second)) || v.RecordedAt.Sub(v.IssuedAt) > 5*time.Second {
		return ErrArtifactOverhead
	}
	for _, s := range []string{v.Owner.DomainID, v.Owner.PolicyDigest, v.Owner.OwnerClientLeaf, v.Owner.OwnerServerLeaf, v.Owner.NativeServerKey} {
		if !bindingFingerprint(s) {
			return ErrArtifactOverhead
		}
	}
	if v.Owner.OwnerClientLeaf == v.Owner.OwnerServerLeaf {
		return ErrArtifactOverhead
	}
	for _, s := range []string{v.Owner.NativeOrigin, v.Owner.NativeScope, v.Owner.NativeServerName, v.Owner.NativeServerVersion} {
		if s == "" || len(s) > 2048 || strings.ContainsAny(s, "\x00\r\n") {
			return ErrArtifactOverhead
		}
	}
	minimum := int64((v.QuotaBytes + (1 << 20) - 1) / (1 << 20))
	if v.Resources.CPU != 0 || v.Resources.RAM != 0 || v.Resources.Runners != 0 || v.Resources.Disk < minimum {
		return ErrArtifactOverhead
	}
	return nil
}
func sameArtifactIdentity(a, b NativeArtifactOverhead) bool {
	return a.Host == b.Host && a.DomainBindingSequence == b.DomainBindingSequence && a.Owner == b.Owner && a.ArtifactID == b.ArtifactID && a.PoolGUID == b.PoolGUID && a.PoolLoadGUID == b.PoolLoadGUID && a.RootGUID == b.RootGUID && a.Dataset == b.Dataset
}
func sqliteNativeArtifactOverheads(tx *sql.Tx) ([]NativeArtifactOverhead, error) {
	var missing bool
	if e := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM audit a WHERE a.kind='native_artifact_overhead' AND NOT EXISTS(SELECT 1 FROM native_artifact_overheads d WHERE d.record_sequence=a.sequence AND d.root_key=a.request_key))").Scan(&missing); e != nil || missing {
		return nil, ErrArtifactOverhead
	}
	type row struct {
		seq                  int64
		key, host, raw, hash string
	}
	latest := map[string]NativeArtifactOverhead{}
	births := map[string]string{}
	var cursor int64
	for {
		rows, e := tx.Query("SELECT record_sequence,root_key,host,CAST(substr(CAST(payload AS BLOB),1,16385) AS TEXT),payload_hash,length(CAST(payload AS BLOB)) FROM native_artifact_overheads WHERE record_sequence>? ORDER BY record_sequence LIMIT 128", cursor)
		if e != nil {
			return nil, e
		}
		var batch []row
		for rows.Next() {
			var r row
			var size int64
			if e = rows.Scan(&r.seq, &r.key, &r.host, &r.raw, &r.hash, &size); e != nil {
				rows.Close()
				return nil, e
			}
			if size < 1 || size > 16384 || int64(len(r.raw)) != size {
				rows.Close()
				return nil, ErrArtifactOverhead
			}
			batch = append(batch, r)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		if len(batch) == 0 {
			break
		}
		for _, r := range batch {
			if r.seq <= cursor {
				return nil, ErrArtifactOverhead
			}
			cursor = r.seq
			var v NativeArtifactOverhead
			if json.Unmarshal([]byte(r.raw), &v) != nil {
				return nil, ErrArtifactOverhead
			}
			raw, hash, e := nativeJSON(v)
			if e != nil || raw != r.raw || hash != r.hash || v.Sequence != r.seq || artifactRootKey(v) != r.key || v.Host != r.host || ValidateNativeArtifactOverhead(v) != nil || !nativeAuditMatches(tx, v.Sequence, "native_artifact_overhead", r.key) {
				return nil, ErrArtifactOverhead
			}
			b, e := nativeDomainRecord(tx, v.DomainBindingSequence)
			if e != nil || b.Spec.Host != v.Host || b.Spec.DomainID != v.Owner.DomainID || b.Spec.PolicyDigest != v.Owner.PolicyDigest || b.Spec.OwnerClientLeaf != v.Owner.OwnerClientLeaf || b.Spec.OwnerServerLeaf != v.Owner.OwnerServerLeaf {
				return nil, ErrArtifactOverhead
			}
			if key, ok := births[v.ArtifactID]; ok && key != r.key {
				return nil, ErrArtifactOverhead
			}
			births[v.ArtifactID] = r.key
			if old, ok := latest[r.key]; ok {
				if !sameArtifactIdentity(old, v) || old.Resources.Disk > v.Resources.Disk || v.RecordedAt.Before(old.RecordedAt) {
					return nil, ErrArtifactOverhead
				}
			} else if len(latest) >= 1000 {
				return nil, ErrArtifactOverhead
			}
			latest[r.key] = v
		}
	}
	out := make([]NativeArtifactOverhead, 0, len(latest))
	for _, v := range latest {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return artifactRootKey(out[i]) < artifactRootKey(out[j]) })
	return out, nil
}
func addArtifactOverheads(out *Allocations, c Config, records []NativeArtifactOverhead) error {
	seen := map[string]bool{}
	for _, v := range records {
		key := artifactRootKey(v)
		if ValidateNativeArtifactOverhead(v) != nil || seen[key] {
			return ErrArtifactOverhead
		}
		seen[key] = true
		if _, ok := c.Hosts[v.Host]; !ok {
			return ErrArtifactOverhead
		}
		h := out.Hosts[v.Host]
		if !accumulate(&out.Global, v.Resources) || !accumulate(&h, v.Resources) || !accumulate(&out.ArtifactOverhead, v.Resources) {
			return ErrArtifactOverhead
		}
		out.Hosts[v.Host] = h
	}
	return nil
}

// RecordNativeArtifactOverhead accepts only the actual pinned owner exchange.
// A record or configured host label can never substitute for this observation.
func (s *SQLiteStore) RecordNativeArtifactOverhead(ctx context.Context, bindingSequence int64, o nativeartifacts.Observation) (NativeArtifactOverhead, error) {
	var out NativeArtifactOverhead
	if !o.Valid() {
		return out, ErrArtifactOverhead
	}
	d := o.Record()
	observedAt := o.ObservedAt()
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		if !o.Valid() || d.QuotaBytes <= 0 {
			return ErrArtifactOverhead
		}
		b, e := nativeDomainRecord(tx, bindingSequence)
		if e != nil {
			return e
		}
		spec := b.Spec
		if d.Owner.DomainID != spec.DomainID || d.Owner.PolicyDigest != spec.PolicyDigest || d.Owner.OwnerClientLeaf != spec.OwnerClientLeaf || d.Owner.OwnerServerLeaf != spec.OwnerServerLeaf {
			return ErrArtifactOverhead
		}
		host, e := scanHostBinding(tx.QueryRow("SELECT host,server_key,origin,scope,server_name,server_version,bound_sequence,bound_at FROM host_bindings WHERE host=?", spec.Host).Scan)
		if e != nil || host.Sequence != spec.HostBindingSequence || host.ServerKeyFingerprint != d.Owner.NativeServerKey || host.OriginFingerprint != d.Owner.NativeOrigin || host.ScopeFingerprint != d.Owner.NativeScope || host.ServerName != d.Owner.NativeServerName || host.ServerVersion != d.Owner.NativeServerVersion {
			return ErrArtifactOverhead
		}
		c, _, _, _, e := sqliteMetadata(tx)
		if e != nil {
			return e
		}
		if _, ok := c.Hosts[spec.Host]; !ok {
			return ErrArtifactOverhead
		}
		out = NativeArtifactOverhead{DomainBindingSequence: bindingSequence, Host: spec.Host, Owner: d.Owner, ArtifactID: d.ArtifactID, Nonce: d.Nonce, IssuedAt: d.IssuedAt, PoolGUID: d.PoolGUID, PoolLoadGUID: d.PoolLoadGUID, RootGUID: d.RootGUID, Dataset: d.Dataset, QuotaBytes: uint64(d.QuotaBytes), Resources: Resources{Disk: int64((uint64(d.QuotaBytes) + (1 << 20) - 1) / (1 << 20))}, ObservedAt: observedAt, RecordedAt: s.now().UTC()}
		records, e := sqliteNativeArtifactOverheads(tx)
		if e != nil {
			return e
		}
		known := false
		for _, old := range records {
			if old.ArtifactID == out.ArtifactID && artifactRootKey(old) != artifactRootKey(out) {
				return ErrArtifactOverhead
			}
			if artifactRootKey(old) == artifactRootKey(out) {
				known = true
				if !sameArtifactIdentity(old, out) {
					return ErrArtifactOverhead
				}
				if old.Resources.Disk >= out.Resources.Disk {
					out = old
					return nil
				}
			}
		}
		if !known && len(records) >= 1000 {
			return ErrArtifactOverhead
		}
		seq, e := nativeAuditSequence(tx, "native_artifact_overhead", artifactRootKey(out), out.RecordedAt)
		if e != nil {
			return e
		}
		out.Sequence = seq
		if ValidateNativeArtifactOverhead(out) != nil {
			return ErrArtifactOverhead
		}
		raw, hash, e := nativeJSON(out)
		if e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO native_artifact_overheads VALUES(?,?,?,?,?)", seq, artifactRootKey(out), out.Host, raw, hash); e != nil {
			return e
		}
		r, e := sqliteReservations(tx)
		if e != nil {
			return e
		}
		i, e := sqliteInventory(tx)
		if e != nil {
			return e
		}
		released, e := sqliteReleasedKeys(tx)
		if e != nil {
			return e
		}
		freed, e := sqliteReuseFreed(tx)
		if e != nil {
			return e
		}
		_, e = sqliteAllocationsWithGrants(tx, c, r, i, released, freed)
		return e
	})
	if e != nil {
		return NativeArtifactOverhead{}, e
	}
	return out, nil
}
func (s *SQLiteStore) checkNativeArtifactOverheads(ctx context.Context) error {
	return s.readTransaction(ctx, func(tx *sql.Tx) error { _, e := sqliteNativeArtifactOverheads(tx); return e })
}
