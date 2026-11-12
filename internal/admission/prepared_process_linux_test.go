package admission

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreparedDispatchProcessWorker(t *testing.T) {
	path := os.Getenv("CAPACITY_PREPARED_TEST_PATH")
	if path == "" {
		return
	}
	ctx := context.Background()
	s, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := os.Getenv("CAPACITY_PREPARED_TEST_KEY")
	plan, err := s.LookupExecutionPlan(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := s.LookupHostBinding(ctx, plan.Request.Host)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := s.LookupOriginalPreparation(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	awaitProcessStart(t)
	row := GarmRowObservation{RequestKey: key, SourceScopeFingerprint: receipt.Source.SourceScopeFingerprint, InstanceID: receipt.Original.InstanceID, Name: receipt.Original.Name, PoolID: receipt.Original.PoolID, ProviderName: receipt.Original.ProviderName, AgentID: receipt.Original.AgentID, Generation: receipt.Original.Generation, ObservedAt: s.now(), PreparationShape: true, CreationEligible: true}
	_, err = s.ExecutePreparedDispatch(ctx, plan, binding, receipt, row, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
		f, err := os.OpenFile(os.Getenv("CAPACITY_PREPARED_TEST_LOG"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return DispatchReceipt{}, err
		}
		_, err = fmt.Fprintln(f, "backend called")
		f.Close()
		return DispatchReceipt{State: "unknown", OperationID: "12345678-1234-1234-1234-123456789abc", ResourceID: plan.Execution.Name}, err
	})
	if !errors.Is(err, ErrDispatchUnknown) && !errors.Is(err, ErrDispatchClaimed) {
		t.Fatal(err)
	}
}
func TestPreparedDispatchSeparateProcessesPreserveOneOriginalUnknownIntent(t *testing.T) {
	s, p, _, receipt, _ := preparedFixture(t)
	log := filepath.Join(t.TempDir(), "backend.log")
	report := runProcessContenders(t, 12, "^TestPreparedDispatchProcessWorker$", func(int) []string {
		return []string{"CAPACITY_PREPARED_TEST_PATH=" + s.path, "CAPACITY_PREPARED_TEST_KEY=" + p.Request.Key, "CAPACITY_PREPARED_TEST_LOG=" + log}
	})
	if report.Processes != 12 || report.Waves != 3 || report.Peak != 4 {
		t.Fatalf("contention changed %+v", report)
	}
	raw, err := os.ReadFile(log)
	if err != nil || strings.Count(string(raw), "backend called") != 1 {
		t.Fatalf("backend calls %q %v", raw, err)
	}
	record, err := s.LookupDispatchReadOnly(context.Background(), p.Request.Key)
	if err != nil || record.State != "unknown" || record.OriginalPreparationSequence != receipt.RecordSequence || record.OperationID == "" {
		t.Fatalf("original unknown provenance lost %+v %v", record, err)
	}
	if sqliteSnapshot(t, s).Allocated.Global.CPU != 1000 {
		t.Fatal("unknown create released capacity")
	}
}
