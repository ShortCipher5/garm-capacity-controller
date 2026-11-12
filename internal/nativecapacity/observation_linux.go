//go:build linux

// Package nativecapacity reads cgroup-v2 quota facts. It does not establish
// physical scope, available capacity, writer fencing, retirement or release.
package nativecapacity

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"os"
	"path"
	"strings"
	"syscall"
)

const cgroup2Magic = 0x63677270
const maxAncestors = 64
const maxFileBytes = 256

// Limit is the smallest reported finite quota among visible ancestors. Empty
// means none was reported, NOT unlimited: inspect Missing as well. CPU is an
// exact rational number of cores; the remaining limits are decimal integers.
// CPU is a bandwidth-period quota, not an instantaneous or dedicated-core bound.
type Limit struct {
	Value string
	At    string
}

type Observation struct {
	CPUQuotaCores    Limit
	MemoryBytes      Limit
	SwapBytes        Limit
	PIDs             Limit
	VisibleAncestors []string
	Missing          []string
}

// Read reads target and every visible ancestor to mountRoot. mountRoot MUST be
// an actual cgroup-v2 filesystem. A namespaced/subtree mount can hide ancestors;
// this function deliberately never reports physical-scope qualification.
// It performs no writes, probes, process movement, allocation or network calls.
func Read(mountRoot, target string) (Observation, error) {
	if !path.IsAbs(mountRoot) {
		return Observation{}, errors.New("absolute mount root required")
	}
	if !fs.ValidPath(target) || target == "." {
		return Observation{}, errors.New("non-root relative cgroup required")
	}
	root, err := os.OpenRoot(mountRoot)
	if err != nil {
		return Observation{}, err
	}
	defer root.Close()
	f, err := root.Open(".")
	if err != nil {
		return Observation{}, err
	}
	defer f.Close()
	var st syscall.Statfs_t
	if err := syscall.Fstatfs(int(f.Fd()), &st); err != nil {
		return Observation{}, err
	}
	if st.Type != cgroup2Magic {
		return Observation{}, errors.New("not a cgroup-v2 filesystem")
	}
	return readVisible(func(name string) (*os.File, error) {
		// Do not mistake a nested ordinary mount for kernel controls. Nonblocking
		// open also avoids waiting on a FIFO supplied by such a mount.
		f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, err
		}
		var fileFS syscall.Statfs_t
		info, statErr := f.Stat()
		fsErr := syscall.Fstatfs(int(f.Fd()), &fileFS)
		if statErr != nil || fsErr != nil || !info.Mode().IsRegular() || fileFS.Type != cgroup2Magic {
			f.Close()
			return nil, errors.New("control is not a regular cgroup-v2 file")
		}
		return f, nil
	}, target)
}

type openFile func(string) (*os.File, error)

func readVisible(open openFile, target string) (Observation, error) {
	if !fs.ValidPath(target) || target == "." {
		return Observation{}, errors.New("non-root relative cgroup required")
	}
	var out Observation
	var cpu, memory, swap, pids *big.Rat
	for current, n := target, 0; ; current, n = path.Dir(current), n+1 {
		if n >= maxAncestors {
			return Observation{}, errors.New("cgroup ancestry exceeds bound")
		}
		kind, err := readFile(open, path.Join(current, "cgroup.type"))
		if current == "." && errors.Is(err, fs.ErrNotExist) {
			// Kernel cgroup.type is a non-root interface. A subtree mount may
			// expose it at '.', so absence is allowed only here and recorded.
			out.Missing = append(out.Missing, "cgroup.type")
		} else if err != nil || kind != "domain" {
			return Observation{}, fmt.Errorf("unsupported or unreadable cgroup type at %s", current)
		}
		out.VisibleAncestors = append(out.VisibleAncestors, current)
		for _, control := range []struct {
			name   string
			cpu    bool
			value  **big.Rat
			result *Limit
		}{
			{"cpu.max", true, &cpu, &out.CPUQuotaCores},
			{"memory.max", false, &memory, &out.MemoryBytes},
			{"memory.swap.max", false, &swap, &out.SwapBytes},
			{"pids.max", false, &pids, &out.PIDs},
		} {
			name := path.Join(current, control.name)
			s, err := readFile(open, name)
			if errors.Is(err, fs.ErrNotExist) {
				out.Missing = append(out.Missing, name)
				continue
			}
			if err != nil {
				return Observation{}, fmt.Errorf("read %s: %w", name, err)
			}
			v, err := parseLimit(s, control.cpu)
			if err != nil {
				return Observation{}, fmt.Errorf("parse %s: %w", name, err)
			}
			if v != nil && (*control.value == nil || v.Cmp(*control.value) < 0) {
				*control.value = v
				*control.result = Limit{Value: v.RatString(), At: current}
			}
		}
		if current == "." {
			break
		}
	}
	return out, nil
}

func readFile(open openFile, name string) (string, error) {
	f, err := open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxFileBytes {
		return "", errors.New("control file exceeds bound")
	}
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" || strings.ContainsAny(s, "\r\n\t") {
		return "", errors.New("invalid control format")
	}
	return s, nil
}
func decimal(s string, zero bool) (*big.Int, error) {
	if s == "" || len(s) > 20 {
		return nil, errors.New("invalid integer")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return nil, errors.New("invalid integer")
		}
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok || n.BitLen() > 64 || (!zero && n.Sign() == 0) {
		return nil, errors.New("out-of-range integer")
	}
	return n, nil
}
func parseLimit(s string, cpu bool) (*big.Rat, error) {
	if cpu {
		parts := strings.Split(s, " ")
		if len(parts) != 2 {
			return nil, errors.New("expected quota and period")
		}
		period, err := decimal(parts[1], false)
		if err != nil {
			return nil, err
		}
		if parts[0] == "max" {
			return nil, nil
		}
		quota, err := decimal(parts[0], false)
		if err != nil {
			return nil, err
		}
		return new(big.Rat).SetFrac(quota, period), nil
	}
	if s == "max" {
		return nil, nil
	}
	n, err := decimal(s, true)
	if err != nil {
		return nil, err
	}
	return new(big.Rat).SetInt(n), nil
}
