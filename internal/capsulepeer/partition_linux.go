//go:build linux

package capsulepeer

import "github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"

type PartitionSpec struct{ Bootstrap, Daemon, Monitors, Guests nativeboundary.Limits }

func (s PartitionSpec) Validate(parent nativeboundary.Limits) error {
	remaining := parent
	for _, v := range []nativeboundary.Limits{s.Bootstrap, s.Daemon, s.Monitors, s.Guests} {
		if v.MemoryBytes <= 0 || v.CPUMilli <= 0 || v.Tasks <= 0 || v.MemoryBytes > remaining.MemoryBytes || v.CPUMilli > remaining.CPUMilli || v.Tasks > remaining.Tasks || v.CPUMilli > (1<<63-1)/100 {
			return ErrPeer
		}
		remaining.MemoryBytes -= v.MemoryBytes
		remaining.CPUMilli -= v.CPUMilli
		remaining.Tasks -= v.Tasks
	}
	return nil
}
