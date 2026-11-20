package managedgarm

import (
	"context"
	"time"
)

// RunStockReadQualification is only the explicit disposable-pilot entry point.
// It accepts no deployment description, credentials, callback, listener or scope.
// All actual dependencies remain fixed and privately loaded by the owned read
// service. It never bootstraps/repairs an installation or returns a capability.
// The fixed ceiling is not a lease or production service uptime guarantee.
func RunStockReadQualification(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrBoundary
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	return serveStockReadOnly(ctx)
}
