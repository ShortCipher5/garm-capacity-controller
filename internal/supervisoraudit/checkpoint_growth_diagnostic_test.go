//go:build linux && cgo

package supervisoraudit

import (
	"fmt"
	"sync"
	"testing"
)

// Diagnostic ONLY: valid paired append batches maximize file growth while preserving
// schema, contiguous facts/audit and MaxFacts. No test or production assertion removed.
func TestCheckpointGrowthDiagnostic(t *testing.T) {
	j, p := setup(t)
	errs := make(chan error, 3)
	start := make(chan struct{})
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		defer close(done)
		d, e := connect(p)
		if e != nil {
			errs <- e
			return
		}
		defer d.Close()
		<-start
		for i := 1; i <= MaxFacts; i += 8 {
			tx, e := d.Begin()
			if e != nil {
				errs <- fmt.Errorf("batch begin: %w", e)
				return
			}
			for n := i; n < i+8; n++ {
				if _, e = tx.Exec("INSERT INTO facts VALUES(?,?,?)", n, claim(n), claim(n+10000)); e == nil {
					_, e = tx.Exec("INSERT INTO audit VALUES(?,?,?)", n, claim(n), claim(n+10000))
				}
				if e != nil {
					tx.Rollback()
					errs <- fmt.Errorf("batch row: %w", e)
					return
				}
			}
			if e = tx.Commit(); e != nil {
				errs <- fmt.Errorf("batch commit: %w", e)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		d, e := connect(p)
		if e != nil {
			errs <- e
			return
		}
		defer d.Close()
		<-start
		for i := 0; i < 1000; i++ {
			if _, e = d.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); e != nil {
				errs <- fmt.Errorf("checkpoint: %w", e)
				return
			}
			select {
			case <-done:
				return
			default:
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 300; i++ {
			k, e := OpenExisting(p, j.HistoryID())
			if e != nil {
				errs <- fmt.Errorf("open: %w", e)
				return
			}
			k.Close()
			select {
			case <-done:
				return
			default:
			}
		}
	}()
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	s, e := j.Snapshot()
	if e != nil || s.Total != MaxFacts {
		t.Fatalf("valid final history failed total=%d error=%v", s.Total, e)
	}
}
