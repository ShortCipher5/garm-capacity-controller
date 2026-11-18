//go:build linux

// capacity-native-domain is the separate host-domain owner. It is not a
// controller subcommand, native API proxy or provider activation switch.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativedomain"
	"golang.org/x/sys/unix"
)

const unit = "garm-capacity-native-domain.service"

var errOwner = errors.New("independently managed native-domain service required")
var properties = []string{"Id", "MainPID", "ControlGroup", "ExitType", "Restart", "Delegate", "KillMode", "SendSIGKILL", "TimeoutStopUSec", "StopWhenUnneeded", "PartOf", "BindsTo", "BoundBy", "ConsistsOf", "PropagatesStopTo", "StopPropagatedFrom", "TriggeredBy", "ExecStop", "ExecStopPost", "ExecReload", "WatchdogUSec", "RuntimeMaxUSec", "RestartForceExitStatus", "OOMPolicy", "Requires", "Requisite", "Wants", "Conflicts", "ConflictedBy", "OnFailure", "OnSuccess", "UpheldBy", "Upholds", "WantedBy", "RequiredBy", "RequisiteOf", "Triggers", "Type", "ExecStartPre", "ExecStartPost", "ExecCondition", "FailureAction", "SuccessAction", "StartLimitAction", "JobTimeoutAction"}

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("capacity-native-domain", flag.ContinueOnError)
	flags.SetOutput(output)
	directory := flags.String("domain-dir", "", "fresh root-owned domain directory")
	binary := flags.String("incusd", "", "qualified root-owned Incus daemon executable")
	helper := flags.String("launcher", "", "qualified root-owned capacity-native-launch executable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *directory == "" || *binary == "" || *helper == "" {
		return errOwner
	}
	if err := verifyManager(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	domain, err := nativedomain.StartFresh(ctx, *directory, *binary, *helper)
	if err != nil {
		return err
	}
	defer domain.Close()
	fmt.Fprintln(output, "Fresh native domain started; allocation and retirement authority remain disabled.")
	// A controller restart has no relationship to this independent service. An
	// explicit stop of THIS service may signal Incus/guests and affect jobs.
	return domain.Wait(ctx)
}

type boundedBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 32768 {
		b.exceeded = true
		return 0, errOwner
	}
	return b.Buffer.Write(p)
}
func verifyManager() error {
	if os.Geteuid() != 0 {
		return errOwner
	}
	// Do not inherit DBUS_SYSTEM_BUS_ADDRESS, SYSTEMD_HOST, SYSTEMD_MACHINE, pager
	// or remote connection settings. Query the actual local manager, not a unit
	// file, PID file, operator assertion or locally installed version string.
	for _, path := range []string{"/", "/usr", "/usr/bin"} {
		i, e := os.Lstat(path)
		if e != nil || !i.IsDir() || i.Mode().Perm()&0022 != 0 || i.Sys().(*syscall.Stat_t).Uid != 0 {
			return errOwner
		}
	}
	info, err := os.Lstat("/usr/bin/systemctl")
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Sys().(*syscall.Stat_t).Uid != 0 {
		return errOwner
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	args := []string{"show", "--all", "--no-pager", "--property=" + strings.Join(properties, ","), unit}
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", args...)
	cmd.Env = []string{"PATH=/usr/bin:/usr/sbin", "LANG=C", "LC_ALL=C"}
	var out boundedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if cmd.Run() != nil || out.exceeded {
		return errOwner
	}
	membership, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return errOwner
	}
	if verifyProperties(out.String(), string(membership), os.Getpid()) != nil {
		return errOwner
	}
	var fs unix.Statfs_t
	if unix.Statfs("/sys/fs/cgroup", &fs) != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return errOwner
	}
	return nil
}
func verifyProperties(data, membership string, pid int) error {
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(data, "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return errOwner
		}
		if _, exists := fields[key]; exists {
			return errOwner
		}
		fields[key] = value
	}
	required := map[string]string{"Id": unit, "MainPID": strconv.Itoa(pid), "ControlGroup": "/system.slice/" + unit, "ExitType": "cgroup", "Restart": "no", "Delegate": "yes", "KillMode": "control-group", "SendSIGKILL": "no", "TimeoutStopUSec": "infinity", "StopWhenUnneeded": "no", "WatchdogUSec": "0", "RuntimeMaxUSec": "infinity", "OOMPolicy": "continue", "Type": "exec", "FailureAction": "none", "SuccessAction": "none", "StartLimitAction": "none", "JobTimeoutAction": "none"}
	for key, value := range required {
		if fields[key] != value {
			return errOwner
		}
	}
	for _, key := range properties {
		if _, ok := fields[key]; !ok {
			return errOwner
		}
	}
	for _, key := range []string{"PartOf", "BindsTo", "BoundBy", "ConsistsOf", "PropagatesStopTo", "StopPropagatedFrom", "TriggeredBy", "ExecStop", "ExecStopPost", "ExecReload", "RestartForceExitStatus", "Requisite", "Wants", "OnFailure", "OnSuccess", "UpheldBy", "Upholds", "WantedBy", "RequiredBy", "RequisiteOf", "Triggers", "ExecStartPre", "ExecStartPost", "ExecCondition"} {
		if fields[key] != "" {
			return errOwner
		}
	}

	// Default system boot/shutdown/root-mount dependencies are expected. Custom
	// Requires/Requisite/Conflicts can couple stop/start to the controller even
	// without PartOf or BindsTo; fail closed rather than accept arbitrary units.
	for key, allowed := range map[string]map[string]bool{
		"Requires":     {"sysinit.target": true, "system.slice": true, "-.mount": true},
		"Conflicts":    {"shutdown.target": true},
		"ConflictedBy": {"shutdown.target": true},
	} {
		for _, dependency := range strings.Fields(fields[key]) {
			if !allowed[dependency] {
				return errOwner
			}
		}
	}
	if strings.TrimSpace(membership) != "0::/system.slice/"+unit {
		return errOwner
	}
	return nil
}
