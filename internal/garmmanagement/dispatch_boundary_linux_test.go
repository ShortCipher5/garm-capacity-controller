package garmmanagement

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

func TestProvenPreDispatchFailureDoesNotLeaveUnknownIntent(t *testing.T) {
	m, j, remote, _ := setup(t)
	ctx := context.Background()
	remote.writeFail = fmt.Errorf("%w: %w", garmapi.ErrManagementNotDispatched, garmapi.ErrManagement)
	if _, err := m.UpdatePool(ctx, requestID, poolID, fingerprint, garmapi.PoolUpdate{MaxRunners: ptr(uint64(3))}, "adjust capacity"); !errors.Is(err, garmapi.ErrManagementNotDispatched) {
		t.Fatal(err)
	}
	status, err := m.Status(ctx)
	if err != nil || status.WriteBlocked || len(status.Pending) != 0 {
		t.Fatal("proven no-dispatch blocked journal", err)
	}
	history, err := j.History(ctx, 0, 10)
	if err != nil || len(history) != 1 || history[0].State != "rejected" {
		t.Fatal("no durable rejection", err)
	}
	remote.writeFail = nil
	if _, err = m.UpdatePool(ctx, nextID, poolID, fingerprint, garmapi.PoolUpdate{MaxRunners: ptr(uint64(3))}, "fresh retry"); err != nil {
		t.Fatal(err)
	}
}
