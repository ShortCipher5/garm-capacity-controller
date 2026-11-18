package main

import (
	"context"
	"testing"
)

func TestPolicyCapStartupExplicitRestrictedConfiguration(t *testing.T) {
	stop, e := startPolicyCapReconciler(context.Background(), configuration{}, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	stop()
	for _, seconds := range []int{-1, 1, 60, 3601} {
		c := configuration{Admin: &adminConfiguration{GarmManagement: &garmManagementConfiguration{PolicyCapIntervalSeconds: seconds}}}
		if _, e = startPolicyCapReconciler(context.Background(), c, nil, nil); e == nil {
			t.Fatal("unqualified startup", seconds)
		}
	}
}
