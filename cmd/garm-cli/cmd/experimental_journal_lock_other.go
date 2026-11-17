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

//go:build !unix

package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// acquireExperimentalJournalLock is the fallback for platforms without a
// kernel advisory lock (flock). It keeps the original O_CREATE|O_EXCL
// semantics: at most one live process can hold the lock file, contention
// fails closed with the holder's recorded pid, and — as before — there is
// no stale-lock reclaim on these platforms, because a contender cannot
// prove a holder dead. Unlike the unix implementation, a holder killed
// with kill -9 leaves the lock file behind; the refusal message says so and
// points at the manual recovery. The embedded record is diagnostics only.
func acquireExperimentalJournalLock() (release func(), err error) {
	lockPath := experimentalJournalLockPath()
	if lockPath == "" {
		return nil, fmt.Errorf("no user configuration directory available for the pending-request journal lock")
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("could not prepare the pending-request journal directory: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err == nil {
		writeExperimentalJournalLockRecord(f)
		_ = f.Sync()
		return func() {
			_ = f.Close()
			// Only the holder ever unlinks the lock file; contenders never
			// do, so they cannot delete a lock a live process acquired.
			_ = os.Remove(lockPath)
		}, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("could not lock the pending-request journal: %w", err)
	}
	data, rErr := os.ReadFile(lockPath)
	return nil, experimentalJournalContentionError(lockPath, data, rErr)
}
