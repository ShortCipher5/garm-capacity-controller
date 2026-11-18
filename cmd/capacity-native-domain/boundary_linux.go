//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativequota"
)

type boundaryConfig struct {
	MemoryBytes       int64  `json:"memory_bytes"`
	CPUMilli          int64  `json:"cpu_milli"`
	Tasks             int64  `json:"tasks"`
	StorageRoot       string `json:"storage_root"`
	StorageDataset    string `json:"storage_dataset"`
	StorageBytes      int64  `json:"storage_bytes"`
	ZFSBinary         string `json:"zfs_binary"`
	ZFSBinarySHA256   string `json:"zfs_binary_sha256"`
	QuotaHelper       string `json:"quota_helper"`
	QuotaHelperSHA256 string `json:"quota_helper_sha256"`
}

func loadBoundary(path string) (boundaryConfig, error) {
	var c boundaryConfig
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return c, errOwner
	}
	for p := path; ; p = filepath.Dir(p) {
		st, e := os.Lstat(p)
		if e != nil || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0022 != 0 {
			return c, errOwner
		}
		s, ok := st.Sys().(*syscall.Stat_t)
		if !ok || s.Uid != 0 {
			return c, errOwner
		}
		if p == path {
			if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
				return c, errOwner
			}
		} else if !st.IsDir() {
			return c, errOwner
		}
		if p == "/" {
			break
		}
	}
	f, e := os.Open(path)
	if e != nil {
		return c, errOwner
	}
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, 8193))
	if e != nil || len(raw) > 8192 {
		return c, errOwner
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.MemoryBytes <= 0 || c.CPUMilli <= 0 || c.Tasks <= 0 || c.StorageBytes <= 0 {
		return c, errOwner
	}
	return c, nil
}

type preparedBoundary struct {
	group   nativeboundary.Observation
	storage nativeboundary.StorageObservation
	reader  *nativequota.Reader
}

func prepareBoundary(ctx context.Context, c boundaryConfig, data string) (*preparedBoundary, error) {
	observer, e := nativeboundary.Observe(nativeboundary.Limits{MemoryBytes: c.MemoryBytes, CPUMilli: c.CPUMilli, Tasks: c.Tasks})
	if e != nil {
		return nil, e
	}
	reader, e := nativequota.New(nativequota.Configuration{Binary: c.ZFSBinary, BinarySHA256: c.ZFSBinarySHA256, Helper: c.QuotaHelper, HelperSHA256: c.QuotaHelperSHA256})
	if e != nil {
		return nil, e
	}
	storage, e := nativeboundary.ObserveStorage(ctx, c.StorageRoot, data, c.StorageDataset, c.StorageBytes, reader)
	if e != nil {
		return nil, e
	}
	// Read-only ZFS work can outlast the observation freshness bound.
	observer, e = nativeboundary.Observe(nativeboundary.Limits{MemoryBytes: c.MemoryBytes, CPUMilli: c.CPUMilli, Tasks: c.Tasks})
	if e != nil {
		return nil, e
	}
	if e = nativeboundary.PlaceOwner(observer); e != nil {
		return nil, e
	}
	return &preparedBoundary{observer, storage, reader}, nil
}
func sameManagerCgroup() bool {
	actual, e := os.Stat("/sys/fs/cgroup")
	if e != nil {
		return false
	}
	host, e := os.Stat("/proc/1/root/sys/fs/cgroup/system.slice/" + unit)
	if e != nil {
		return false
	}
	a, ok := actual.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	b, ok := host.Sys().(*syscall.Stat_t)
	return ok && a.Dev == b.Dev && a.Ino == b.Ino
}

// boundedUnit emits the actual manager configuration used to create the private
// namespace and finite ancestor before this process is born. It never installs,
// enables, stops or restarts a service. Existing workloads are not touched.
func boundedUnit(c boundaryConfig, data, config string) (string, error) {
	for _, p := range []string{c.StorageRoot, data, config} {
		if !unitPath(p) {
			return "", errOwner
		}
	}
	if c.MemoryBytes <= 0 || c.CPUMilli <= 0 || c.Tasks <= 0 || c.StorageBytes <= 0 || !strings.HasPrefix(data, c.StorageRoot+"/") {
		return "", errOwner
	}
	return fmt.Sprintf(`[Unit]
Description=GARM capacity bounded fresh native owner (experimental)
StopWhenUnneeded=no

[Service]
Type=exec
ExitType=cgroup
User=root
Group=root
UMask=0077
ExecStart=/usr/libexec/garm-capacity/capacity-native-domain --domain-dir=%s --incusd=/usr/libexec/incus/incusd --launcher=/usr/libexec/garm-capacity/capacity-native-launch --boundary-config=%s
Restart=no
Delegate=yes
ProtectControlGroups=private
ProtectSystem=strict
ReadWritePaths=%s
MemoryMax=%d
MemorySwapMax=0
CPUQuota=%d.%d%%
CPUQuotaPeriodSec=100ms
TasksMax=%d
KillMode=control-group
SendSIGKILL=no
TimeoutStopSec=infinity
OOMPolicy=continue
StandardOutput=null
StandardError=null
`, data, config, c.StorageRoot, c.MemoryBytes, c.CPUMilli/10, c.CPUMilli%10, c.Tasks), nil
}
func unitPath(p string) bool {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
		return false
	}
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '/' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}
