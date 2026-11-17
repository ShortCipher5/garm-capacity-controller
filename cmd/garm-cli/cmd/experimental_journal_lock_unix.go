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

//go:build unix

package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// acquireExperimentalJournalLock exclusively locks the pending-request
// journal for the calling process and returns a release func. Every
// read-modify-write of the journal (load, binding decision, journal write
// and the dispatch it guards, including --resolve and pending clear) runs
// while holding this lock, so two garm-cli processes can never both journal
// or both resolve.
//
// The lock is a kernel advisory lock (flock) on a persistent lock file
// adjacent to the journal. The kernel releases it the moment the holding
// process dies — including kill -9 — so a contender can never observe, and
// must never try to "reclaim", a stale lock: a second flock fails while ANY
// live process (or its still-open descriptor) holds it, and succeeds
// immediately once the last holder is gone. The lock file is never
// unlinked; the embedded pid record is written for diagnostics only.
//
// It fails closed: any state that cannot be proven safe results in an error
// and no journal access at all.
func acquireExperimentalJournalLock() (release func(), err error) {
	lockPath := experimentalJournalLockPath()
	if lockPath == "" {
		return nil, fmt.Errorf("no user configuration directory available for the pending-request journal lock")
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("could not prepare the pending-request journal directory: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("could not open the pending-request journal lock: %w", err)
	}
	locked := false
	defer func() {
		if !locked {
			_ = f.Close()
		}
	}()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			data, rErr := os.ReadFile(lockPath)
			return nil, experimentalJournalContentionError(lockPath, data, rErr)
		}
		return nil, fmt.Errorf("could not lock the pending-request journal: %w", err)
	}
	locked = true
	writeExperimentalJournalLockRecord(f)
	// The lock file only carries diagnostics; its content need not survive a
	// crash, so it is deliberately not fsynced.
	return func() { _ = f.Close() }, nil
}
