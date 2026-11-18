//go:build linux

package nativequota

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const rootAccountingProperties = "guid,type,quota,used,available,usedbysnapshots,usedbydataset,usedbychildren,usedbyrefreservation"

type RootAccounting struct {
	dataset, guid                                                      string
	quota, used, available, snapshots, datasetUsed, children, reserved uint64
	at                                                                 time.Time
}

func (o RootAccounting) Dataset() string            { return o.dataset }
func (o RootAccounting) GUID() string               { return o.guid }
func (o RootAccounting) Quota() uint64              { return o.quota }
func (o RootAccounting) Used() uint64               { return o.used }
func (o RootAccounting) Available() uint64          { return o.available }
func (o RootAccounting) UsedBySnapshots() uint64    { return o.snapshots }
func (o RootAccounting) UsedByDataset() uint64      { return o.datasetUsed }
func (o RootAccounting) UsedByChildren() uint64     { return o.children }
func (o RootAccounting) UsedByReservations() uint64 { return o.reserved }
func (o RootAccounting) ObservedAt() time.Time      { return o.at }
func parseRootAccounting(raw []byte, want FilesystemIdentity) (RootAccounting, error) {
	if !validDataset(want.dataset) || want.guid == "" || want.quota <= 0 || len(raw) == 0 || len(raw) > 4096 || raw[len(raw)-1] != '\n' {
		return RootAccounting{}, ErrQuota
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 || f[0] != want.dataset || values[f[1]] != "" {
			return RootAccounting{}, ErrQuota
		}
		values[f[1]] = f[2]
	}
	if len(values) != 9 || values["guid"] != want.guid || values["type"] != "filesystem" {
		return RootAccounting{}, ErrQuota
	}
	n := map[string]uint64{}
	for _, k := range []string{"quota", "used", "available", "usedbysnapshots", "usedbydataset", "usedbychildren", "usedbyrefreservation"} {
		v, e := strconv.ParseUint(values[k], 10, 64)
		if e != nil || strconv.FormatUint(v, 10) != values[k] {
			return RootAccounting{}, ErrQuota
		}
		n[k] = v
	}
	if n["quota"] != uint64(want.quota) {
		return RootAccounting{}, ErrQuota
	}
	total := uint64(0)
	for _, k := range []string{"usedbysnapshots", "usedbydataset", "usedbychildren", "usedbyrefreservation"} {
		if n[k] > n["used"]-total {
			return RootAccounting{}, ErrQuota
		}
		total += n[k]
	}
	if total != n["used"] {
		return RootAccounting{}, ErrQuota
	}
	return RootAccounting{want.dataset, want.guid, n["quota"], n["used"], n["available"], n["usedbysnapshots"], n["usedbydataset"], n["usedbychildren"], n["usedbyrefreservation"], time.Now().UTC()}, nil
}
func (r *Reader) readRootAccounting(ctx context.Context, root FilesystemIdentity) (RootAccounting, error) {
	if r == nil || ctx == nil || os.Geteuid() != 0 || !validDataset(root.dataset) {
		return RootAccounting{}, ErrQuota
	}
	binary, e := pinned(r.config.Binary, r.config.BinarySHA256)
	if e != nil {
		return RootAccounting{}, e
	}
	defer binary.Close()
	helper, e := pinned(r.config.Helper, r.config.HelperSHA256)
	if e != nil {
		return RootAccounting{}, e
	}
	defer helper.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/proc/self/fd/4", "--root-accounting", root.dataset)
	cmd.ExtraFiles = []*os.File{binary, helper}
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	var out limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if cmd.Run() != nil || ctx.Err() != nil {
		return RootAccounting{}, ErrQuota
	}
	return parseRootAccounting(out.Bytes(), root)
}
