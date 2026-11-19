package managedgarm

import (
	"os"
	"syscall"
	"testing"
)

func TestManagedRestartNeverStartsBeforeAllPeersStopped(t *testing.T) {
	s, _ := topologyFixture()
	order, err := managedRestartOrder(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 6 || order[0].Container != s.IngressID || order[1].Container != s.ControllerID || order[2].Container != s.GarmID || order[3].Container != s.GarmID || order[4].Container != s.ControllerID || order[5].Container != s.IngressID {
		t.Fatal(order)
	}
	r := restartRecord{Version: "managed-restart/v1", Specification: s, Steps: order, State: "prepared"}
	pending, err := advanceRestart(r, true, false)
	if err != nil || !pending.Pending {
		t.Fatal(err)
	}
	if _, err := advanceRestart(pending, true, true); err == nil {
		t.Fatal("unknown action retried")
	}
	for i := 0; i < 6; i++ {
		r, err = advanceRestart(r, true, true)
		if err != nil || r.Completed != i+1 || r.Pending {
			t.Fatal(i, err)
		}
	}
	if _, err := advanceRestart(r, true, true); err == nil {
		t.Fatal("unexpected seventh transition")
	}
}

func TestRestartConfigSetRefusesExtraMountedCredentials(t *testing.T) {
	files := map[string]sealedFileIdentity{".": {}, "garm.toml": {}, "api-cert.pem": {}, "api-key.pem": {}}
	if validateSealedConfigSet(files, nil) != nil {
		t.Fatal("base set refused")
	}
	files["native-owner-key.pem"] = sealedFileIdentity{}
	if validateSealedConfigSet(files, nil) == nil {
		t.Fatal("extra secret allowed")
	}
	delete(files, "native-owner-key.pem")
	delete(files, "api-key.pem")
	if validateSealedConfigSet(files, nil) == nil {
		t.Fatal("missing config allowed")
	}
}

type deploymentInfo struct {
	os.FileInfo
	uid uint32
}

func (d deploymentInfo) Sys() any { return &syscall.Stat_t{Uid: d.uid} }
func TestControlledRestartRequiresUntraversableRootDeployment(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if privateDeploymentParent(deploymentInfo{info, 0}) {
		t.Fatal("other UID can traverse host config parent")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !privateDeploymentParent(deploymentInfo{info, 0}) {
		t.Fatal("private root directory refused")
	}
	if privateDeploymentParent(deploymentInfo{info, 65530}) {
		t.Fatal("provider-owned deployment accepted")
	}
}
