//go:build linux

package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativedomain"
)

func validProperties() string {
	values := map[string]string{"Id": unit, "MainPID": "42", "ControlGroup": "/system.slice/" + unit, "ExitType": "cgroup", "Restart": "no", "Delegate": "yes", "KillMode": "control-group", "SendSIGKILL": "no", "TimeoutStopUSec": "infinity", "StopWhenUnneeded": "no", "WatchdogUSec": "0", "RuntimeMaxUSec": "infinity", "OOMPolicy": "continue", "Type": "exec", "FailureAction": "none", "SuccessAction": "none", "StartLimitAction": "none", "JobTimeoutAction": "none"}
	var out strings.Builder
	for _, key := range properties {
		out.WriteString(key + "=" + values[key] + "\n")
	}
	return out.String()
}
func TestManagerContractRejectsControllerScopeAndUnsafeLifecycle(t *testing.T) {
	input := validProperties()
	membership := "0::/system.slice/" + unit + "\n"
	if verifyProperties(strings.Replace(strings.Replace(input, "Requires=\n", "Requires=sysinit.target system.slice -.mount\n", 1), "Conflicts=\n", "Conflicts=shutdown.target\n", 1), membership, 42) != nil {
		t.Fatal("expected system defaults rejected")
	}
	if verifyProperties(input, membership, 42) != nil {
		t.Fatal("valid manager contract rejected")
	}
	for _, pair := range [][2]string{{"ExecStartPost=\n", "ExecStartPost=/bin/false\n"}, {"FailureAction=none", "FailureAction=reboot"}, {"Type=exec", "Type=notify"}, {"UpheldBy=\n", "UpheldBy=controller.service\n"}, {"WantedBy=\n", "WantedBy=controller.service\n"}, {"RequiredBy=\n", "RequiredBy=controller.service\n"}, {"Triggers=\n", "Triggers=controller.service\n"}, {"Requires=\n", "Requires=controller.service\n"}, {"Requisite=\n", "Requisite=controller.service\n"}, {"Conflicts=\n", "Conflicts=controller.service\n"}, {"ConflictedBy=\n", "ConflictedBy=controller.service\n"}, {"OnFailure=\n", "OnFailure=cleanup.service\n"}, {"ExitType=cgroup", "ExitType=main"}, {"Restart=no", "Restart=always"}, {"Delegate=yes", "Delegate=no"}, {"PartOf=\n", "PartOf=controller.service\n"}, {"BoundBy=\n", "BoundBy=controller.service\n"}, {"StopPropagatedFrom=\n", "StopPropagatedFrom=controller.service\n"}, {"SendSIGKILL=no", "SendSIGKILL=yes"}, {"TimeoutStopUSec=infinity", "TimeoutStopUSec=90s"}, {"OOMPolicy=continue", "OOMPolicy=kill"}, {"ExecStop=\n", "ExecStop=/bin/kill\n"}, {"MainPID=42", "MainPID=43"}, {"ControlGroup=/system.slice/" + unit, "ControlGroup=/system.slice/controller.service"}, {"WatchdogUSec=0", "WatchdogUSec=30s"}} {
		if verifyProperties(strings.Replace(input, pair[0], pair[1], 1), membership, 42) == nil {
			t.Fatal("accepted unsafe property", pair)
		}
	}
	if verifyProperties(input, "0::/system.slice/controller.service\n", 42) == nil {
		t.Fatal("accepted controller cgroup")
	}
	if verifyProperties(input, membership, 43) == nil {
		t.Fatal("accepted foreign owner PID")
	}
	if verifyProperties(input+"ExitType=cgroup\n", membership, 42) == nil {
		t.Fatal("duplicate property")
	}
	if verifyProperties(strings.Replace(input, "ExitType=cgroup\n", "", 1), membership, 42) == nil {
		t.Fatal("unsupported manager version accepted")
	}
}
func TestCLIRejectsMissingArgumentsAndRootlessService(t *testing.T) {
	if run(nil, io.Discard) == nil {
		t.Fatal("missing arguments accepted")
	}
	if os.Geteuid() != 0 && run([]string{"--domain-dir=/nonexistent", "--incusd=/nonexistent", "--launcher=/nonexistent"}, io.Discard) == nil {
		t.Fatal("rootless host service accepted")
	}
}
func TestUnitHasNoAutomaticActivationOrControllerCoupling(t *testing.T) {
	data, err := os.ReadFile("../../deploy/native-domain/" + unit)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, line := range []string{"Type=exec", "ExitType=cgroup", "Restart=no", "Delegate=yes", "KillMode=control-group", "SendSIGKILL=no", "TimeoutStopSec=infinity", "OOMPolicy=continue", "StateDirectoryMode=0700"} {
		if !strings.Contains(text, "\n"+line+"\n") {
			t.Fatal("missing unit boundary", line)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		for _, forbidden := range []string{"[Install]", "PartOf=", "BindsTo=", "Requires=", "Wants=", "ExecStop=", "ExecStopPost=", "ExecReload=", "RestartForceExitStatus=", "RuntimeMaxSec=", "WatchdogSec="} {
			if strings.HasPrefix(line, forbidden) {
				t.Fatal("unexpected lifecycle coupling", line)
			}
		}
	}
}
func TestNoZeroDomainWaitAuthority(t *testing.T) {
	var d nativedomain.Domain
	if d.Wait(context.Background()) != nativedomain.ErrUnknown {
		t.Fatal("zero domain wait authority")
	}
}
