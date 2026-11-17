package incusapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// HostTotals is a bounded standalone-host capacity snapshot. Every zero value
// means unknown or unparseable, never a confirmed absence: this is a display
// snapshot, not an admission-grade observation.
type HostTotals struct {
	CPUMillisTotal int64
	RAMMiBTotal    int64
	DiskMiBTotal   int64
	DiskMiBUsed    int64
	// Storage pool totals are present only when a pool is configured on the
	// client; StoragePool stays empty and the totals stay zero otherwise.
	StoragePool             string
	StoragePoolDiskMiBTotal int64
	StoragePoolDiskMiBUsed  int64
}

// Guest is one instance in a bounded project-scoped inventory. Zero resource
// values mean the guest could not be bounded from its configuration (for
// example unmanaged guests without limits); Controller, Pool and Reservation
// stay empty for guests the runner controller does not attribute.
type Guest struct {
	Name        string
	Status      string
	CPUMillis   int64
	RAMMiB      int64
	DiskMiBRoot int64
	CreatedAt   time.Time
	Controller  string
	Pool        string
	Reservation string
}

// UtilizationSample aggregates MEASURED utilization from guest state across
// one sweep. It is distinct from budget accounting: these values are observed
// usage, not allocated or reserved capacity.
type UtilizationSample struct {
	CPUUsedMillis int64
	RAMUsedMiB    int64
	DiskUsedMiB   int64
	// Guests counts only guests whose state contributed to the totals.
	Guests int
	// Errors lists guests whose state fetch failed; their contribution is
	// unknown and excluded instead of failing the whole sample.
	Errors []GuestUtilizationError
}

type GuestUtilizationError struct {
	Guest string
	Err   error
}

func bytesToMiB(bytes int64) int64 {
	if bytes <= 0 {
		return 0
	}
	// Ceiling division, matching the package's memory parsing convention.
	return (bytes-1)/(1<<20) + 1
}

// WithStoragePool returns a copy of the client that additionally reports the
// named storage pool totals from HostTotals. The pool name must be a valid
// Incus name; an empty name clears the configuration.
func (c *Client) WithStoragePool(pool string) (*Client, error) {
	if pool != "" && !name(pool) {
		return nil, ErrConfiguration
	}
	copy := *c
	copy.storagePool = pool
	return &copy, nil
}

// HostTotals reads one bounded resource snapshot from a verified standalone
// server. Unrecognized or malformed resource entries degrade to zero (unknown)
// instead of an error; the trusted standalone check still fails closed.
func (c *Client) HostTotals(ctx context.Context) (HostTotals, error) {
	if err := c.trusted(ctx, false); err != nil {
		return HostTotals{}, err
	}
	response, err := c.request(ctx, http.MethodGet, "/1.0/resources", nil, nil)
	if err != nil {
		return HostTotals{}, err
	}
	if response.kind != "sync" {
		return HostTotals{}, ErrResponse
	}
	fields, err := object(response.metadata)
	if err != nil {
		return HostTotals{}, err
	}
	var result HostTotals
	if raw, ok := fields["cpu"]; ok {
		if cpu, err := object(raw); err == nil {
			if total, err := field[int64](cpu, "total"); err == nil && total > 0 && total <= int64(1<<63-1)/1000 {
				result.CPUMillisTotal = total * 1000
			}
		}
	}
	if raw, ok := fields["memory"]; ok {
		if memory, err := object(raw); err == nil {
			if total, err := field[int64](memory, "total"); err == nil {
				result.RAMMiBTotal = bytesToMiB(total)
			}
		}
	}
	if raw, ok := fields["disk"]; ok {
		var entries []json.RawMessage
		if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '[' && json.Unmarshal(raw, &entries) == nil {
			for _, entryRaw := range entries {
				entry, err := object(entryRaw)
				if err != nil {
					continue
				}
				if size, err := field[int64](entry, "size"); err == nil {
					result.DiskMiBTotal += bytesToMiB(size)
				}
				if used, err := field[int64](entry, "usage"); err == nil {
					result.DiskMiBUsed += bytesToMiB(used)
				}
			}
		}
	}
	if c.storagePool != "" {
		if raw, ok := fields["storage_pools"]; ok {
			if pools, err := object(raw); err == nil {
				if entryRaw, ok := pools[c.storagePool]; ok {
					if entry, err := object(entryRaw); err == nil {
						if spaceRaw, ok := entry["space"]; ok {
							if space, err := object(spaceRaw); err == nil {
								result.StoragePool = c.storagePool
								if total, err := field[int64](space, "total"); err == nil {
									result.StoragePoolDiskMiBTotal = bytesToMiB(total)
								}
								if used, err := field[int64](space, "used"); err == nil {
									result.StoragePoolDiskMiBUsed = bytesToMiB(used)
								}
							}
						}
					}
				}
			}
		}
	}
	return result, nil
}

// Guests lists every instance in the configured project, including guests the
// runner controller does not manage (empty attribution), so operators can spot
// unmanaged capacity. Resource limits and root-disk size are best-effort: zero
// means unbounded-from-view, never evidence of absence. Row identity and shape
// stay strict: malformed rows, duplicate names and invalid attribution fail closed.
func (c *Client) Guests(ctx context.Context) ([]Guest, error) {
	if err := c.trusted(ctx, false); err != nil {
		return nil, err
	}
	response, err := c.request(ctx, http.MethodGet, "/1.0/instances", url.Values{"recursion": {"1"}}, nil)
	if err != nil {
		return nil, err
	}
	if response.kind != "sync" {
		return nil, ErrResponse
	}
	var rows []json.RawMessage
	trimmed := bytes.TrimSpace(response.metadata)
	if len(trimmed) == 0 || trimmed[0] != '[' || json.Unmarshal(response.metadata, &rows) != nil {
		return nil, ErrResponse
	}
	result := make([]Guest, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		guest, err := parseGuest(row)
		if err != nil {
			return nil, err
		}
		if seen[guest.Name] {
			return nil, ErrResponse
		}
		seen[guest.Name] = true
		result = append(result, guest)
	}
	return result, nil
}

func parseGuest(row json.RawMessage) (Guest, error) {
	fields, err := object(row)
	if err != nil {
		return Guest{}, err
	}
	guest := Guest{}
	guest.Name, err = field[string](fields, "name")
	if err != nil || !name(guest.Name) {
		return Guest{}, ErrResponse
	}
	guest.Status, err = field[string](fields, "status")
	if err != nil || !label(guest.Status) {
		return Guest{}, ErrResponse
	}
	created, err := field[string](fields, "created_at")
	if err != nil || !label(created) {
		return Guest{}, ErrResponse
	}
	if guest.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		if guest.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
			return Guest{}, ErrResponse
		}
	}
	if raw, ok := fields["expanded_config"]; ok {
		config, err := object(raw)
		if err != nil {
			return Guest{}, err
		}
		if cpuRaw, ok := config["limits.cpu"]; ok {
			var value string
			if json.Unmarshal(cpuRaw, &value) == nil {
				if cpu, err := parseCPU(value); err == nil {
					guest.CPUMillis = cpu
				}
			}
		}
		if memoryRaw, ok := config["limits.memory"]; ok {
			var value string
			if json.Unmarshal(memoryRaw, &value) == nil {
				if memory, err := parseMemory(value); err == nil {
					guest.RAMMiB = memory
				}
			}
		}
		for key, target := range map[string]*string{"user.runner-controller-id": &guest.Controller, "user.runner-pool-id": &guest.Pool, "user.garm-capacity.reservation": &guest.Reservation} {
			if raw, ok := config[key]; ok {
				if json.Unmarshal(raw, target) != nil || !label(*target) {
					return Guest{}, ErrResponse
				}
			}
		}
	}
	if raw, ok := fields["expanded_devices"]; ok {
		devices, err := object(raw)
		if err != nil {
			return Guest{}, err
		}
		if rootRaw, ok := devices["root"]; ok {
			root, err := object(rootRaw)
			if err != nil {
				return Guest{}, err
			}
			if sizeRaw, ok := root["size"]; ok {
				var value string
				if json.Unmarshal(sizeRaw, &value) == nil {
					if size, err := parseMemory(value); err == nil {
						guest.DiskMiBRoot = size
					}
				}
			}
		}
	}
	return guest, nil
}

// SampleUtilization measures current utilization from every guest's instance
// state and aggregates it. One guest failing (transport, response or parse)
// marks only that guest's contribution unknown; the sweep still returns the
// summed utilization of the remaining guests with a per-guest error entry.
func (c *Client) SampleUtilization(ctx context.Context) (UtilizationSample, error) {
	guests, err := c.Guests(ctx)
	if err != nil {
		return UtilizationSample{}, err
	}
	var result UtilizationSample
	for _, guest := range guests {
		usage, err := c.guestUsage(ctx, guest.Name)
		if err != nil {
			result.Errors = append(result.Errors, GuestUtilizationError{Guest: guest.Name, Err: err})
			continue
		}
		result.CPUUsedMillis += usage.cpuMillis
		result.RAMUsedMiB += usage.ramMiB
		result.DiskUsedMiB += usage.diskMiB
		result.Guests++
	}
	return result, nil
}

type guestUsage struct {
	cpuMillis int64
	ramMiB    int64
	diskMiB   int64
}

func (c *Client) guestUsage(ctx context.Context, guestName string) (guestUsage, error) {
	if !name(guestName) {
		return guestUsage{}, ErrConfiguration
	}
	response, err := c.request(ctx, http.MethodGet, "/1.0/instances/"+guestName+"/state", nil, nil)
	if err != nil {
		return guestUsage{}, err
	}
	if response.kind != "sync" {
		return guestUsage{}, ErrResponse
	}
	fields, err := object(response.metadata)
	if err != nil {
		return guestUsage{}, err
	}
	var result guestUsage
	if raw, ok := fields["cpu"]; ok {
		if cpu, err := object(raw); err == nil {
			if usage, err := field[int64](cpu, "usage"); err == nil && usage >= 0 {
				// Instance state reports cumulative CPU time in nanoseconds.
				result.cpuMillis = usage / int64(time.Millisecond)
			}
		}
	}
	if raw, ok := fields["memory"]; ok {
		if memory, err := object(raw); err == nil {
			if usage, err := field[int64](memory, "usage"); err == nil {
				result.ramMiB = bytesToMiB(usage)
			}
		}
	}
	if raw, ok := fields["disk"]; ok {
		var entries []json.RawMessage
		if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '[' && json.Unmarshal(raw, &entries) == nil {
			for _, entryRaw := range entries {
				entry, err := object(entryRaw)
				if err != nil {
					continue
				}
				usageRaw, ok := entry["usage"]
				if !ok {
					continue
				}
				usage, err := object(usageRaw)
				if err != nil {
					continue
				}
				if used, err := field[int64](usage, "usage_bytes"); err == nil {
					result.diskMiB += bytesToMiB(used)
				}
			}
		}
	}
	return result, nil
}
