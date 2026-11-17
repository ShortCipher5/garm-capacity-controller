// Copyright 2025 Cloudbase Solutions SRL
//
//	Licensed under the Apache License, Version 2.0 (the "License"); you may
//	not use this file except in compliance with the License. You may obtain
//	a copy of the License at
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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// This file holds the cross-process safety gear for the single-slot
// pending-request journal: an exclusive advisory lock file adjacent to the
// journal, plus the profile/server/entity/pool binding check that keeps a
// journaled request tied to the exact invocation context that dispatched it.

// experimentalJournalLockPath names the advisory lock file adjacent to the
// pending-request journal. The lock is acquired with O_CREATE|O_EXCL, so at
// most one live process can hold it; its body records who does.
func experimentalJournalLockPath() string {
	path := experimentalPendingPath()
	if path == "" {
		return ""
	}
	return path + ".lock"
}

// experimentalJournalLockRecord is the lock file body: enough to tell a
// contender which process holds the journal and since when, so it can decide
// between waiting and reclaiming a provably stale lock.
type experimentalJournalLockRecord struct {
	PID        int    `json:"pid"`
	AcquiredAt string `json:"acquired_at"`
}

// acquireExperimentalJournalLock exclusively locks the pending-request
// journal for the calling process and returns a release func. Every
// read-modify-write of the journal (load, binding decision, journal write
// and the dispatch it guards, including --resolve) runs while holding this
// lock, so two garm-cli processes can never both journal or both resolve.
//
// It fails closed: any state that cannot be proven safe results in an error
// and no journal access at all. On contention the holder record is read; a
// lock held by a process that no longer exists is reclaimed and the
// acquisition is retried exactly once, anything else is refused.
func acquireExperimentalJournalLock() (release func(), err error) {
	lockPath := experimentalJournalLockPath()
	if lockPath == "" {
		return nil, fmt.Errorf("no user configuration directory available for the pending-request journal lock")
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("could not prepare the pending-request journal directory: %w", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			rec := experimentalJournalLockRecord{
				PID:        os.Getpid(),
				AcquiredAt: time.Now().UTC().Format(time.RFC3339),
			}
			if data, mErr := json.Marshal(rec); mErr == nil {
				_, _ = f.Write(data)
			}
			_ = f.Sync()
			_ = f.Close()
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("could not lock the pending-request journal: %w", err)
		}

		data, rErr := os.ReadFile(lockPath)
		if rErr != nil {
			if errors.Is(rErr, os.ErrNotExist) {
				continue // the holder released between attempts; try once more
			}
			return nil, fmt.Errorf("the pending-request journal is locked, but the lock record could not be read: %w", rErr)
		}
		var holder experimentalJournalLockRecord
		if jErr := json.Unmarshal(data, &holder); jErr != nil || holder.PID <= 0 {
			return nil, fmt.Errorf("the pending-request journal is locked, but the lock record %s is unreadable; refusing to guess who holds it (remove it by hand only when no garm-cli process is running)", lockPath)
		}
		if experimentalProcessAlive(holder.PID) {
			return nil, fmt.Errorf("the pending-request journal is locked by garm-cli process %d (lock acquired at %s); another write or resolution is in flight there — wait for it to finish, or resolve that pending request first", holder.PID, holder.AcquiredAt)
		}
		// The holder no longer exists: reclaim the stale lock and retry the
		// acquisition exactly once (loop bound), never more.
		if dErr := os.Remove(lockPath); dErr != nil && !errors.Is(dErr, os.ErrNotExist) {
			return nil, fmt.Errorf("the pending-request journal lock held by dead process %d could not be reclaimed: %w", holder.PID, dErr)
		}
	}
	return nil, fmt.Errorf("the pending-request journal lock could not be acquired even after reclaiming a stale lock; refusing to proceed unprotected")
}

// experimentalProcessAlive reports whether pid currently names a live
// process. The staleness check is built on /proc and is therefore only
// meaningful on Linux; anywhere else a contender cannot prove a lock stale,
// so every holder is treated as alive and contention fails closed.
func experimentalProcessAlive(pid int) bool {
	if runtime.GOOS != "linux" {
		return true
	}
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return err == nil
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
