package garmapi

import "testing"

func TestDemandSourceIdentityMatchesManagementDestination(t *testing.T) {
	c := demandFixture(t, "[]", nil)
	defer c.Close()
	want := c.discovery.management.ManagementIdentity()
	if got := c.SourceIdentity(); got == "" || got != want {
		t.Fatalf("source identity %q differs from ownership identity %q", got, want)
	}
	var missing *DemandClient
	if missing.SourceIdentity() != "" || (&DemandClient{}).SourceIdentity() != "" {
		t.Fatal("incomplete client supplied an identity")
	}
	other := &DemandClient{discovery: &PoolDiscoveryClient{management: &ManagementClient{base: "https://other.example/api/v1"}}}
	if other.SourceIdentity() == want {
		t.Fatal("different destination shared identity")
	}
}
