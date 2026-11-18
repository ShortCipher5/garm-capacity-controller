package main

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
	"testing"
)

func TestJobObserverOptInBeforeCredentialReads(t *testing.T) {
	c := configuration{Admin: &adminConfiguration{}, JobObserver: &jobObserverConfiguration{CAFile: "/must-not-read", TokenFile: "/must-not-read", IntervalSeconds: 60, LookbackSeconds: 3600, Repositories: []demandrepair.Repository{{GarmID: "repo"}}, Owners: []nativeStockHostConfiguration{{}}}}
	if _, _, e := buildJobObserver(c, nil); e == nil {
		t.Fatal("missing optin accepted")
	}
	c.EnableJobObserver = true
	c.Admin = nil
	if validateJobObserver(c) == nil {
		t.Fatal("missing status/auth listener accepted")
	}
	c = configuration{}
	service, close, e := buildJobObserver(c, nil)
	if e != nil || service != nil {
		t.Fatal(e)
	}
	close()
	c.EnableJobObserver = true
	if validateJobObserver(c) == nil {
		t.Fatal("optin without configuration accepted")
	}
}
