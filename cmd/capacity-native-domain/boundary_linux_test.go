//go:build linux

package main

import (
	"strings"
	"testing"
)

func TestBoundedUnitCreatesFinitePrivateNamespaceWithoutEnable(t *testing.T) {
	c := boundaryConfig{StorageRoot: "/var/lib/native", MemoryBytes: 1 << 30, CPUMilli: 1500, Tasks: 100, StorageBytes: 1 << 30}
	s, e := boundedUnit(c, "/var/lib/native/new", "/etc/native/boundary.json")
	if e != nil {
		t.Fatal(e)
	}
	for _, line := range []string{"ProtectControlGroups=private", "ProtectSystem=strict", "ReadWritePaths=/var/lib/native", "MemoryMax=1073741824", "MemorySwapMax=0", "CPUQuota=150.0%", "TasksMax=100", "Restart=no", "StandardError=null"} {
		if !strings.Contains(s, line+"\n") {
			t.Fatal(line)
		}
	}
	for _, bad := range []string{"[Install]", "PartOf=", "BindsTo=", "ExecStop="} {
		if strings.Contains(s, bad) {
			t.Fatal(bad)
		}
	}
	if _, e := boundedUnit(c, "/var/lib/native/new%bad", "/etc/native.json"); e == nil {
		t.Fatal("systemd specifier injection")
	}
	if _, e := boundedUnit(c, "/elsewhere/new", "/etc/native.json"); e == nil {
		t.Fatal("outside dataset")
	}
}
func TestPrivateManagerModeDoesNotAcceptHostRootOrUnconfinedUnit(t *testing.T) {
	good := strings.Replace(strings.Replace(validProperties(), "ProtectControlGroups=\n", "ProtectControlGroups=private\n", 1), "ProtectSystem=\n", "ProtectSystem=strict\n", 1)
	if verifyPropertiesMode(good, "0::/\n", 42, true) != nil {
		t.Fatal("private contract")
	}
	if verifyPropertiesMode(good, "0::/system.slice/"+unit, 42, true) == nil {
		t.Fatal("host cg")
	}
	if verifyPropertiesMode(strings.Replace(good, "ProtectSystem=strict", "ProtectSystem=no", 1), "0::/", 42, true) == nil {
		t.Fatal("writable host")
	}
}
