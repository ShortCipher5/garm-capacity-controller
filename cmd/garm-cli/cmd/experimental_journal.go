// Copyright 2025 Cloudbase Solutions SRL
//
//	Licensed under the Apache License, Version 2.0 (the "License"); you may
//	not use this file except in compliance with the License. You may obtain a
//	copy of the License at
//
//	     http://www.apache.org/licenses/LICENSE-2.0
//
//	Unless required by applicable law or agreed to in writing, software
//	distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
//	WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
//	License for the specific language governing permissions and limitations
//	under the License.

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// This file holds the cross-process safety gear for the single-slot
// pending-request journal: an exclusive advisory lock on a persistent lock
// file adjacent to the journal (platform implementation in
// experimental_journal_lock_unix.go / experimental_journal_lock_other.go),
// plus the profile/server/entity/pool binding check that keeps a journaled
// request tied to the exact invocation context that dispatched it.
//
// The lock file itself is never deleted, by design: where the platform
// offers a kernel advisory lock, the kernel releases it when the holding
// process dies — even on kill -9 — so there is no stale lock to reap and
// no window in which one process could delete a lock another live process
// legitimately acquired. The pid record embedded in the file is diagnostics
// for the refusal message only; the kernel lock is authoritative.

// experimentalJournalLockPath names the advisory lock file adjacent to the
// pending-request journal.
func experimentalJournalLockPath() string {
	path := experimentalPendingPath()
	if path == "" {
		return ""
	}
	return path + ".lock"
}

// experimentalJournalLockRecord is the lock file body: enough to tell a
// contender which process holds the journal and since when. It is written
// for diagnostics only; acquisition is enforced by the platform lock, never
// by interpreting this record.
type experimentalJournalLockRecord struct {
	PID        int    `json:"pid"`
	AcquiredAt string `json:"acquired_at"`
}

// writeExperimentalJournalLockRecord refreshes the diagnostics record of a
// lock file this process has already acquired.
func writeExperimentalJournalLockRecord(f *os.File) {
	rec := experimentalJournalLockRecord{
		PID:        os.Getpid(),
		AcquiredAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt(data, 0)
}

// experimentalJournalContentionError builds the refusal a contender gets
// while another live process holds the journal lock. data/readErr are the
// best-effort read of the holder record; an unreadable record never weakens
// the refusal, it only loses the holder details.
func experimentalJournalContentionError(lockPath string, data []byte, readErr error) error {
	if readErr != nil {
		return fmt.Errorf("the pending-request journal is locked by another garm-cli process, but the lock record could not be read (%v); refusing to guess who holds it (remove %s by hand only when no garm-cli process is running)", readErr, lockPath)
	}
	var holder experimentalJournalLockRecord
	if err := json.Unmarshal(data, &holder); err != nil || holder.PID <= 0 {
		return fmt.Errorf("the pending-request journal is locked by another garm-cli process, but the lock record %s is unreadable; refusing to guess who holds it (remove it by hand only when no garm-cli process is running)", lockPath)
	}
	return fmt.Errorf("the pending-request journal is locked by garm-cli process %d (lock acquired at %s); another write or resolution is in flight there — wait for it to finish, or resolve that pending request first", holder.PID, holder.AcquiredAt)
}

// experimentalCurrentBinding captures the invocation context a pending
// request is bound to: the active profile name (mgr is the effective manager
// after initConfig's active/first fallback) and the effective base URL of
// the client that would actually carry the request.
func experimentalCurrentBinding(client *rawClient) (profile, server string) {
	profile = mgr.Name
	if client != nil {
		server = client.BaseURL
	}
	return profile, server
}

// pendingBindingMismatch describes why the journaled request does not belong
// to the current invocation, or "" when it does. Used by the fresh-write
// path, where the operator supplies entity/pool explicitly.
func pendingBindingMismatch(rec *experimentalPendingRecord, profile, server string, ref poolSettingsRef) string {
	var parts []string
	if rec.Profile != profile {
		parts = append(parts, fmt.Sprintf("profile %q (current %q)", rec.Profile, profile))
	}
	if rec.Server != server {
		parts = append(parts, fmt.Sprintf("server %q (current %q)", rec.Server, server))
	}
	if rec.EntityType != ref.entityType || rec.EntityID != ref.entityID {
		parts = append(parts, fmt.Sprintf("%s %q (current %s %q)", rec.EntityType, rec.EntityID, ref.entityType, ref.entityID))
	}
	if rec.PoolID != ref.poolID {
		parts = append(parts, fmt.Sprintf("pool %q (current %q)", rec.PoolID, ref.poolID))
	}
	return strings.Join(parts, ", ")
}

// pendingBindingMismatchResolve is the --resolve variant: resolution
// re-sends the journaled entity/pool verbatim, so only the profile/server
// binding can mismatch.
func pendingBindingMismatchResolve(rec *experimentalPendingRecord, profile, server string) string {
	return pendingBindingMismatch(rec, profile, server, poolSettingsRef{
		entityType: rec.EntityType,
		entityID:   rec.EntityID,
		poolID:     rec.PoolID,
	})
}
