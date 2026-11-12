package admission

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Keep total contenders, but bound concurrent race-enabled Go runtimes. Each
// wave opens its ledger BEFORE declaring ready and waits for one shared gate.
// This is real simultaneous cross-process contention, not sequential testing.
const processContentionWidth = 4

type processContentionReport struct{ Processes, Waves, Peak int }

func awaitProcessStart(t *testing.T) {
	t.Helper()
	gate, ready := os.Getenv("GCC_PROCESS_TEST_GATE"), os.Getenv("GCC_PROCESS_TEST_READY")
	if gate == "" && ready == "" {
		return
	}
	if gate == "" || ready == "" {
		t.Fatal("incomplete test-only process barrier")
	}
	if runtime.GOMAXPROCS(0) != 2 {
		t.Fatal("helper scheduler width was not bounded")
	}
	marker, err := os.OpenFile(ready, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := marker.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		data, err := os.ReadFile(gate)
		if err == nil {
			if string(data) != "go" {
				t.Fatal("invalid test barrier release")
			}
			return
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("test process barrier release timed out")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func runProcessContenders(t *testing.T, total int, worker string, environment func(int) []string) processContentionReport {
	t.Helper()
	if total < 2 || worker == "" {
		t.Fatal("contention requires multiple processes and an explicit worker")
	}
	report := processContentionReport{}
	for first := 0; first < total; first += processContentionWidth {
		width := min(processContentionWidth, total-first)
		runProcessWave(t, first, width, worker, environment)
		report.Processes += width
		report.Waves++
		report.Peak = max(report.Peak, width)
	}
	return report
}

func runProcessWave(t *testing.T, first, width int, worker string, environment func(int) []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	type result struct {
		index  int
		output []byte
		err    error
	}
	finished := make(chan result, width)
	completed := 0
	defer func() {
		cancel()
		deadline := time.NewTimer(2 * time.Second)
		defer deadline.Stop()
		for completed < width {
			select {
			case <-finished:
				completed++
			case <-deadline.C:
				return
			}
		}
	}()
	directory := t.TempDir()
	gate := filepath.Join(directory, "release")
	ready := make([]string, width)
	for offset := 0; offset < width; offset++ {
		i := first + offset
		ready[offset] = filepath.Join(directory, fmt.Sprintf("ready-%d", i))
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run="+worker)
		cmd.Env = append(os.Environ(), environment(i)...)
		// Last value wins through os/exec environment de-duplication. Do not inherit
		// a large machine's scheduler width into every helper runtime.
		cmd.Env = append(cmd.Env, "GOMAXPROCS=2", "GCC_PROCESS_TEST_GATE="+gate, "GCC_PROCESS_TEST_READY="+ready[offset])
		go func(index int) { output, err := cmd.CombinedOutput(); finished <- result{index, output, err} }(i)
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(2 * time.Millisecond)
	defer poll.Stop()
	for {
		count := 0
		for _, path := range ready {
			if _, err := os.Stat(path); err == nil {
				count++
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
		// Exiting before the gate is a failed control, even with status zero.
		select {
		case result := <-finished:
			completed++
			t.Fatalf("worker %d exited before barrier: %v %s", result.index, result.err, result.output)
		default:
		}
		if count == width {
			break
		}
		select {
		case result := <-finished:
			completed++
			t.Fatalf("worker %d failed before barrier: %v %s", result.index, result.err, result.output)
		case <-deadline.C:
			t.Fatal("not every process reached the contention barrier")
		case <-ctx.Done():
			t.Fatal("process contention timed out")
		case <-poll.C:
		}
	}
	// Publish the complete gate atomically: creating the final file and then
	// writing it lets a ready child observe an empty transient release.
	pending := gate + ".pending"
	if err := os.WriteFile(pending, []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(pending, gate); err != nil {
		t.Fatal(err)
	}
	for completed < width {
		select {
		case result := <-finished:
			completed++
			if result.err != nil {
				t.Errorf("worker %d: %v %s", result.index, result.err, result.output)
			}
		case <-ctx.Done():
			t.Fatal("released process failed to finish")
		}
	}
}

// This negative control prevents an accidentally sequential/ungated harness
// from being reported as successful concurrent contention.
func TestContentionHarnessNonparticipant(t *testing.T) {}
func TestContentionHarnessControlDriver(t *testing.T) {
	if os.Getenv("GCC_PROCESS_TEST_CONTROL") != "1" {
		return
	}
	runProcessContenders(t, 4, "^TestContentionHarnessNonparticipant$", func(int) []string { return nil })
}
func TestContentionHarnessRejectsNonparticipants(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestContentionHarnessControlDriver$")
	cmd.Env = append(os.Environ(), "GCC_PROCESS_TEST_CONTROL=1", "GOMAXPROCS=2")
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("negative-control driver failed to terminate")
	}
	if err == nil || (!strings.Contains(string(output), "exited before barrier") && !strings.Contains(string(output), "failed before barrier")) {
		t.Fatalf("ungated processes were accepted: %v %s", err, output)
	}
}
