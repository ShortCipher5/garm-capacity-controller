package main

import (
	"context"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/brokerinput"
)

// Static checks and actual startup agree on the configured shape; neither
// passing the check nor setting the interval establishes live write authority.
func validatePolicyCapConfiguration(c configuration) error {
	if c.Admin == nil || c.Admin.GarmManagement == nil || c.Admin.GarmManagement.PolicyCapIntervalSeconds == 0 {
		return nil
	}
	a := c.Admin
	n := a.GarmManagement.PolicyCapIntervalSeconds
	if n < 60 || n > 3600 || !a.EnableExperimentalManagement || !a.EnableExperimentalGarmManagement || !a.GarmManagement.ManagedSupervisor ||
		validateManagedSettingsRouting(c) != nil {
		return adminview.ErrConfiguration
	}
	return nil
}

// Explicit opt-in only. This is not grant-loop activation or upward scaling.
func startPolicyCapReconciler(ctx context.Context, c configuration, store *admission.SQLiteStore, view adminview.GarmManager) (func(), error) {
	noop := func() {}
	if c.Admin == nil || c.Admin.GarmManagement == nil || c.Admin.GarmManagement.PolicyCapIntervalSeconds == 0 {
		return noop, nil
	}
	a := c.Admin
	n := a.GarmManagement.PolicyCapIntervalSeconds
	if ctx == nil || validatePolicyCapConfiguration(c) != nil {
		return noop, adminview.ErrConfiguration
	}
	consumer, ok := view.(interface{ SetPolicyCapService(*brokerinput.CapService) })
	if !ok {
		return noop, adminview.ErrConfiguration
	}
	client, e := managedSettingsClient(a)
	if e != nil {
		return noop, e
	}
	service, e := brokerinput.NewCapService(store, client, time.Duration(n)*time.Second)
	if e != nil {
		client.Close()
		return noop, e
	}
	consumer.SetPolicyCapService(service)
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = service.Run(run) }()
	return func() { cancel(); <-done; client.Close() }, nil
}
