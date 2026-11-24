package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeenforcement"
	"sort"
	"strings"
	"time"
)

var ErrDomainOverhead = errors.New("canonical native domain overhead unavailable; retain all charges")

const sqliteNativeDomainOverheadSchema = `
CREATE TABLE native_domain_overheads(record_sequence INTEGER PRIMARY KEY NOT NULL REFERENCES audit(sequence),domain_id TEXT NOT NULL,host TEXT NOT NULL,payload TEXT NOT NULL CHECK(length(CAST(payload AS BLOB)) BETWEEN 1 AND 16384),payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64)) STRICT;
CREATE TRIGGER native_domain_overhead_immutable BEFORE UPDATE ON native_domain_overheads BEGIN SELECT RAISE(ABORT,'domain overhead immutable'); END;
CREATE TRIGGER native_domain_overhead_permanent BEFORE DELETE ON native_domain_overheads BEGIN SELECT RAISE(ABORT,'domain overhead permanent'); END;
DROP TRIGGER reservation_current_accounting;
CREATE TRIGGER reservation_current_accounting BEFORE INSERT ON reservations WHEN NEW.accounting_version!=26 BEGIN SELECT RAISE(ABORT,'current domain overhead accounting required'); END;
DROP TRIGGER dispatch_current_accounting;
CREATE TRIGGER dispatch_current_accounting BEFORE INSERT ON dispatches WHEN NEW.grant_accounting_version!=26 BEGIN SELECT RAISE(ABORT,'current domain overhead accounting required'); END;
PRAGMA user_version=26;
`

func overheadResources(d nativeenforcement.Descriptor) (Resources, error) {
	digest, e := nativeenforcement.DigestLimits(d.Limits)
	if e != nil || digest != d.LimitsDigest {
		return Resources{}, ErrDomainOverhead
	}
	const mib = uint64(1 << 20)
	return Resources{CPU: int64(d.Limits.CPUMilli), RAM: int64((d.Limits.MemoryBytes + mib - 1) / mib), Disk: int64((d.Limits.DiskBytes + mib - 1) / mib)}, nil
}
func overheadDominates(a, b Resources) bool {
	return a.CPU >= b.CPU && a.RAM >= b.RAM && a.Disk >= b.Disk && a.Runners == 0 && b.Runners == 0
}

// ValidateNativeDomainOverhead validates a detached projection only. It neither
// authenticates a caller-provided record nor authorizes any physical release.
func ValidateNativeDomainOverhead(v NativeDomainOverhead) error {
	r, e := overheadResources(v.Evidence)
	d := v.Evidence
	for _, value := range []string{d.Owner.PolicyDigest, d.Owner.OwnerClientLeaf, d.Owner.OwnerServerLeaf, d.Owner.NativeServerKey} {
		if !bindingFingerprint(value) {
			return ErrDomainOverhead
		}
	}
	for _, value := range []string{d.Owner.NativeOrigin, d.Owner.NativeScope, d.Owner.NativeServerName, d.Owner.NativeServerVersion} {
		if value == "" || len(value) > 2048 || strings.ContainsAny(value, "\x00\r\n") {
			return ErrDomainOverhead
		}
	}
	if d.Owner.OwnerClientLeaf == d.Owner.OwnerServerLeaf || v.DomainBindingSequence >= v.Sequence {
		return ErrDomainOverhead
	}
	if e != nil || v.Sequence <= 0 || v.DomainBindingSequence <= 0 || !bindingName(v.Host) || !bindingFingerprint(v.DomainID) || !bindingFingerprint(v.CapsuleBirthID) || v.DomainID != d.Owner.DomainID || v.CapsuleBirthID != v.DomainID || v.CapsuleBirthID != d.CapsuleBirthID || d.Version != nativeenforcement.Version || !bindingFingerprint(d.Nonce) || !bindingFingerprint(d.RootImageSHA256) || !bindingFingerprint(d.DeviceProgramSHA256) || !bindingFingerprint(d.EgressPolicySHA256) || v.RecordedAt.IsZero() || d.IssuedAt.IsZero() || d.IssuedAt.After(v.RecordedAt.Add(time.Second)) || v.RecordedAt.Sub(d.IssuedAt) > 5*time.Second || !overheadDominates(v.Resources, r) {
		return ErrDomainOverhead
	}
	return nil
}
func sqliteNativeDomainOverheads(tx *sql.Tx) ([]NativeDomainOverhead, error) {
	type row struct {
		seq                     int64
		domain, host, raw, hash string
	}
	var missing bool
	if e := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM audit a WHERE a.kind='native_domain_overhead' AND NOT EXISTS(SELECT 1 FROM native_domain_overheads d WHERE d.record_sequence=a.sequence AND d.domain_id=a.request_key))").Scan(&missing); e != nil || missing {
		return nil, ErrDomainOverhead
	}
	latest := map[string]NativeDomainOverhead{}
	var cursor int64
	for {
		rows, e := tx.Query("SELECT record_sequence,domain_id,host,CAST(substr(CAST(payload AS BLOB),1,16385) AS TEXT),payload_hash,length(CAST(payload AS BLOB)) FROM native_domain_overheads WHERE record_sequence>? ORDER BY record_sequence LIMIT 128", cursor)
		if e != nil {
			return nil, e
		}
		var all []row
		for rows.Next() {
			var r row
			var size int64
			if e = rows.Scan(&r.seq, &r.domain, &r.host, &r.raw, &r.hash, &size); e != nil {
				rows.Close()
				return nil, e
			}
			if size < 1 || size > 16384 || int64(len(r.raw)) != size {
				rows.Close()
				return nil, ErrDomainOverhead
			}
			all = append(all, r)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		if len(all) == 0 {
			break
		}
		for _, r := range all {
			if r.seq <= cursor {
				return nil, ErrDomainOverhead
			}
			cursor = r.seq

			var v NativeDomainOverhead
			if json.Unmarshal([]byte(r.raw), &v) != nil {
				return nil, ErrDomainOverhead
			}
			raw, hash, e := nativeJSON(v)
			if e != nil || raw != r.raw || hash != r.hash || v.Sequence != r.seq || v.DomainID != r.domain || v.Host != r.host || ValidateNativeDomainOverhead(v) != nil || !nativeAuditMatches(tx, v.Sequence, "native_domain_overhead", v.DomainID) {
				return nil, ErrDomainOverhead
			}
			b, e := nativeDomainRecord(tx, v.DomainBindingSequence)
			if e != nil || b.Spec.Host != v.Host || b.Spec.DomainID != v.DomainID || b.Spec.PolicyDigest != v.Evidence.Owner.PolicyDigest || b.Spec.OwnerClientLeaf != v.Evidence.Owner.OwnerClientLeaf || b.Spec.OwnerServerLeaf != v.Evidence.Owner.OwnerServerLeaf {
				return nil, ErrDomainOverhead
			}
			if p, ok := latest[v.DomainID]; ok {
				if p.CapsuleBirthID != v.CapsuleBirthID || p.DomainBindingSequence != v.DomainBindingSequence || !overheadDominates(v.Resources, p.Resources) || v.RecordedAt.Before(p.RecordedAt) {
					return nil, ErrDomainOverhead
				}
			}
			if _, ok := latest[v.DomainID]; !ok && len(latest) >= 1000 {
				return nil, ErrDomainOverhead
			}
			latest[v.DomainID] = v
		}
	}
	out := make([]NativeDomainOverhead, 0, len(latest))
	for _, v := range latest {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DomainID < out[j].DomainID })
	return out, nil
}
func addDomainOverheads(out *Allocations, c Config, records []NativeDomainOverhead) error {
	seen := map[string]bool{}
	for _, v := range records {
		if ValidateNativeDomainOverhead(v) != nil || seen[v.DomainID] {
			return ErrDomainOverhead
		}
		seen[v.DomainID] = true
		if _, ok := c.Hosts[v.Host]; !ok {
			return ErrDomainOverhead
		}
		h := out.Hosts[v.Host]
		if !accumulate(&out.Global, v.Resources) || !accumulate(&h, v.Resources) || !accumulate(&out.DomainOverhead, v.Resources) {
			return ErrDomainOverhead
		}
		out.Hosts[v.Host] = h
	}
	return nil
}

// RecordNativeDomainOverhead records real enforced whole-capsule ceilings, not
// measured usage. This only adds conservative charges; it does not assert that
// host-side artifacts outside the capsule are bounded or that a guest is absent.
func (s *SQLiteStore) RecordNativeDomainOverhead(ctx context.Context, bindingSequence int64, o nativeenforcement.Observation) (NativeDomainOverhead, error) {
	var out NativeDomainOverhead
	if !o.Valid() {
		return out, ErrDomainOverhead
	}
	d := o.Record()
	r, e := overheadResources(d)
	if e != nil {
		return out, e
	}
	e = s.transaction(ctx, func(tx *sql.Tx) error {
		if !o.Valid() {
			return ErrDomainOverhead
		}
		b, e := nativeDomainRecord(tx, bindingSequence)
		if e != nil {
			return e
		}
		spec := b.Spec
		if d.Owner.DomainID != spec.DomainID || d.Owner.PolicyDigest != spec.PolicyDigest || d.Owner.OwnerClientLeaf != spec.OwnerClientLeaf || d.Owner.OwnerServerLeaf != spec.OwnerServerLeaf {
			return ErrDomainOverhead
		}
		host, e := scanHostBinding(tx.QueryRow("SELECT host,server_key,origin,scope,server_name,server_version,bound_sequence,bound_at FROM host_bindings WHERE host=?", spec.Host).Scan)
		if e != nil || host.Sequence != spec.HostBindingSequence || host.ServerKeyFingerprint != d.Owner.NativeServerKey || host.OriginFingerprint != d.Owner.NativeOrigin || host.ScopeFingerprint != d.Owner.NativeScope || host.ServerName != d.Owner.NativeServerName || host.ServerVersion != d.Owner.NativeServerVersion {
			return ErrDomainOverhead
		}
		c, _, _, _, e := sqliteMetadata(tx)
		if e != nil {
			return e
		}
		if _, ok := c.Hosts[spec.Host]; !ok {
			return ErrDomainOverhead
		}
		records, e := sqliteNativeDomainOverheads(tx)
		if e != nil {
			return e
		}
		knownDomain := false
		for _, old := range records {
			if old.DomainID == spec.DomainID {
				knownDomain = true
				if old.CapsuleBirthID != d.CapsuleBirthID || old.DomainBindingSequence != bindingSequence {
					return ErrDomainOverhead
				}
				if overheadDominates(old.Resources, r) {
					out = old
					return nil
				}
				r.CPU = max(r.CPU, old.Resources.CPU)
				r.RAM = max(r.RAM, old.Resources.RAM)
				r.Disk = max(r.Disk, old.Resources.Disk)
			}
		}
		if !knownDomain && len(records) >= 1000 {
			return ErrDomainOverhead
		}
		now := s.now().UTC()
		seq, e := nativeAuditSequence(tx, "native_domain_overhead", spec.DomainID, now)
		if e != nil {
			return e
		}
		out = NativeDomainOverhead{Sequence: seq, DomainBindingSequence: bindingSequence, Host: spec.Host, DomainID: spec.DomainID, CapsuleBirthID: d.CapsuleBirthID, Resources: r, Evidence: d, RecordedAt: now}
		if ValidateNativeDomainOverhead(out) != nil {
			return ErrDomainOverhead
		}
		raw, hash, e := nativeJSON(out)
		if e != nil {
			return e
		}
		_, e = tx.Exec("INSERT INTO native_domain_overheads VALUES(?,?,?,?,?)", seq, spec.DomainID, spec.Host, raw, hash)
		if e != nil {
			return e
		}
		reservations, e := sqliteReservations(tx)
		if e != nil {
			return e
		}
		inventory, e := sqliteInventory(tx)
		if e != nil {
			return e
		}
		released, e := sqliteReleasedKeys(tx)
		if e != nil {
			return e
		}
		// Reject arithmetic overflow atomically, but retain genuine over-budget
		// charges: exceeding a configured budget is not permission to erase debt.
		_, e = sqliteAllocationsWithGrants(tx, c, reservations, inventory, released)
		return e
	})
	if e != nil {
		return NativeDomainOverhead{}, e
	}
	return out, nil
}
func (s *SQLiteStore) checkNativeDomainOverheads(ctx context.Context) error {
	return s.readTransaction(ctx, func(tx *sql.Tx) error { _, e := sqliteNativeDomainOverheads(tx); return e })
}
