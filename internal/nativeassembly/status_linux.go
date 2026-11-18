//go:build linux

package nativeassembly

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeplanning"
	"os"
	"path/filepath"
)

// RecoveryStatus describes local durable assembly state only. It is not the
// preparation registry, a live worker indicator, or permission to retry writes.
type RecoveryStatus struct {
	TargetID             string  `json:"target_id"`
	PreparationID        string  `json:"preparation_id"`
	SelectionDigest      string  `json:"selection_digest"`
	State                string  `json:"state"`
	PartialOutputPresent bool    `json:"partial_output_present"`
	Assembly             *Report `json:"assembly,omitempty"`
	ActivationAuthorized bool    `json:"activation_authorized"`
}

func absent(path string) bool { _, e := os.Lstat(path); return os.IsNotExist(e) }

// Status checks the actual saved selection and protected journal without creating
// even a lock file. Malformed, orphaned or inconsistent state is unavailable,
// never reported as fresh/retryable. Snapshots do not assert a worker is running.
func (s *Service) Status(ctx context.Context, id, selectionDigest string) (RecoveryStatus, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || !hash(selectionDigest) {
		return RecoveryStatus{}, ErrAssembly
	}
	if !s.mu.TryLock() {
		return RecoveryStatus{}, ErrAssembly
	}
	defer s.mu.Unlock()
	r, ok := s.recipes[id]
	if !ok || privateDirectory(r.OutputParent) != nil {
		return RecoveryStatus{}, ErrAssembly
	}
	history, e := s.planning.SavedSelections(id)
	if e != nil {
		return RecoveryStatus{}, ErrAssembly
	}
	found := false
	for _, saved := range history {
		if saved.SelectionDigest == selectionDigest && nativeplanning.Digest(saved.Selection.Binding) == nativeplanning.Digest(r.Binding) {
			found = true
			break
		}
	}
	if !found {
		return RecoveryStatus{}, ErrAssembly
	}
	status := RecoveryStatus{TargetID: id, PreparationID: r.Binding.PreparationID, SelectionDigest: selectionDigest, State: "never_attempted"}
	intentPath := filepath.Join(r.OutputParent, r.stem(selectionDigest)+".intent.json")
	receiptPath := filepath.Join(r.OutputParent, r.stem(selectionDigest)+".receipt.json")
	lockPath := filepath.Join(r.OutputParent, "native-assembly.lock")
	if absent(lockPath) {
		if !absent(intentPath) || !absent(receiptPath) || !absent(r.output(selectionDigest)) || !absent(lockPath) || ctx.Err() != nil {
			return RecoveryStatus{}, ErrAssembly
		}
		return status, nil
	}
	held, e := lock(r, false)
	if e != nil {
		return RecoveryStatus{}, ErrAssembly
	}
	defer held.Close()
	if absent(intentPath) {
		if !absent(receiptPath) || !absent(r.output(selectionDigest)) || ctx.Err() != nil {
			return RecoveryStatus{}, ErrAssembly
		}
		return status, nil
	}
	intent, e := readRecord(intentPath)
	expected := record{Version: version, RecipeDigest: nativeplanning.Digest(r), Report: Report{TargetID: id, PreparationID: r.Binding.PreparationID, SelectionDigest: selectionDigest, State: "assembly_unknown"}}
	if e != nil || intent != expected {
		return RecoveryStatus{}, ErrAssembly
	}
	if absent(receiptPath) {
		st, e := os.Lstat(r.output(selectionDigest))
		if e == nil {
			if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || privateDirectory(r.output(selectionDigest)) != nil {
				return RecoveryStatus{}, ErrAssembly
			}
			status.PartialOutputPresent = true
		} else if !os.IsNotExist(e) {
			return RecoveryStatus{}, ErrAssembly
		}
		if ctx.Err() != nil {
			return RecoveryStatus{}, ErrAssembly
		}
		status.State = "assembly_unknown"
		return status, nil
	}
	completed, e := inspectCompletedLocked(ctx, id, selectionDigest, r)
	if e != nil {
		return RecoveryStatus{}, ErrAssembly
	}
	report := completed.Report()
	status.State = "completed_bundle"
	status.Assembly = &report
	return status, nil
}
