package incusapi

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
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

type guestInventory struct {
	Guest
	container bool
	// Exact named disk devices described by this inventory row, or nil
	// when the inventory cannot qualify native disk report coverage.
	measurableDisks map[string]struct{}
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
	// Errors lists guests with an unreadable state or at least one
	// unavailable metric. Other reported counters still contribute. A
	// sample with Errors is incomplete, not a zero or a rate baseline.
	Errors []GuestUtilizationError
	// GuestCPU is optional in-process diagnostic evidence for detecting
	// changes, not proof of an immutable counter epoch. Nil means some
	// listed guest did not qualify; a nonnil empty slice is a read of an
	// empty project inventory. Never publish or persist guest identities.
	GuestCPU      []GuestCPUObservation `json:"-"`
	guestCPUScope guestCPUObservedScope
}

// GuestCPUObservation is one particular container's reported process and
// cumulative native CPU counter. Incus reads these fields sequentially;
// matching observations cannot establish that an epoch stayed unchanged.
type GuestCPUObservation struct {
	Name                 string
	CreatedAt, StartedAt time.Time
	PID, CPUUsedNanos    int64
}

type guestCPUObservedScope struct {
	project, peer string
}

// GuestCPUObservedScopeChanged detects differing locally observed TLS peers
// or project scopes. False, including any absent scope, is not a trust or
// continuity verdict. Hosts remain separately configured by the existing caller.
func (s UtilizationSample) GuestCPUObservedScopeChanged(other UtilizationSample) bool {
	return s.guestCPUScope.peer != "" && other.guestCPUScope.peer != "" && s.guestCPUScope != other.guestCPUScope
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
	inventory, _, err := c.readGuestInventory(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Guest, 0, len(inventory))
	for _, guest := range inventory {
		result = append(result, guest.Guest)
	}
	return result, nil
}

func (c *Client) readGuestInventory(ctx context.Context) ([]guestInventory, string, error) {
	root, err := c.trustedRoot(ctx, false)
	if err != nil {
		return nil, "", err
	}
	response, err := c.request(ctx, http.MethodGet, "/1.0/instances", url.Values{"recursion": {"1"}}, nil)
	if err != nil {
		return nil, "", err
	}
	if response.kind != "sync" {
		return nil, "", ErrResponse
	}
	var rows []json.RawMessage
	trimmed := bytes.TrimSpace(response.metadata)
	if len(trimmed) == 0 || trimmed[0] != '[' || json.Unmarshal(response.metadata, &rows) != nil {
		return nil, "", ErrResponse
	}
	result := make([]guestInventory, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		guest, err := parseGuest(row)
		if err != nil {
			return nil, "", err
		}
		if seen[guest.Name] {
			return nil, "", ErrResponse
		}
		seen[guest.Name] = true
		result = append(result, guest)
	}
	peer := response.serverKeyFingerprint
	if peer != root.serverKeyFingerprint {
		// Keep ordinary inventory and display behavior, but this root
		// did not witness the same origin as the guest inventory.
		peer = ""
	}
	return result, peer, nil
}

func parseGuest(row json.RawMessage) (guestInventory, error) {
	fields, err := object(row)
	if err != nil {
		return guestInventory{}, err
	}
	var result guestInventory
	guest := &result.Guest
	guest.Name, err = field[string](fields, "name")
	if err != nil || !name(guest.Name) {
		return guestInventory{}, ErrResponse
	}
	guest.Status, err = field[string](fields, "status")
	if err != nil || !label(guest.Status) {
		return guestInventory{}, ErrResponse
	}
	created, err := field[string](fields, "created_at")
	if err != nil || !label(created) {
		return guestInventory{}, ErrResponse
	}
	if guest.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		if guest.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
			return guestInventory{}, ErrResponse
		}
	}
	result.container = qualifiesGuestCPUContainer(fields)
	if raw, ok := fields["expanded_config"]; ok {
		config, err := object(raw)
		if err != nil {
			return guestInventory{}, err
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
					return guestInventory{}, ErrResponse
				}
			}
		}
	}
	if raw, ok := fields["expanded_devices"]; ok {
		devices, err := object(raw)
		if err != nil {
			return guestInventory{}, err
		}
		result.measurableDisks = expectedGuestStateDisks(devices)
		if rootRaw, ok := devices["root"]; ok {
			root, err := object(rootRaw)
			if err != nil {
				return guestInventory{}, err
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
	return result, nil
}

// expectedGuestStateDisks identifies the root and pool-backed devices that
// can appear in native Incus disk state. Mounts without a pool, such as host
// paths, are not measured by that API. Missing or ambiguous device inventory
// cannot qualify complete native reports.
func expectedGuestStateDisks(devices map[string]json.RawMessage) map[string]struct{} {
	result := make(map[string]struct{})
	roots := 0
	for deviceName, raw := range devices {
		device, err := object(raw)
		if err != nil || !label(deviceName) {
			return nil
		}
		kind, err := field[string](device, "type")
		if err != nil || kind == "" {
			return nil
		}
		if kind != "disk" {
			continue
		}
		var path, pool string
		if _, present := device["path"]; present {
			path, err = field[string](device, "path")
			if err != nil {
				return nil
			}
		}
		if _, present := device["pool"]; present {
			pool, err = field[string](device, "pool")
			if err != nil {
				return nil
			}
		}
		if path == "/" {
			roots++
			if pool == "" {
				return nil
			}
		}
		if pool != "" {
			result[deviceName] = struct{}{}
		}
	}
	if roots != 1 {
		return nil
	}
	return result
}

// SampleUtilization sums reported usage from project guests. It does not
// report physical host usage, prove runtime continuity or deduplicate storage
// that may be shared by guests. Incomplete measurements are marked even if
// some counters remain available for display.
func (c *Client) SampleUtilization(ctx context.Context) (UtilizationSample, error) {
	guests, peer, err := c.readGuestInventory(ctx)
	if err != nil {
		return UtilizationSample{}, err
	}
	var result UtilizationSample
	guestCPU := make([]GuestCPUObservation, 0, len(guests))
	cpuQualified := peer != ""
	for _, guest := range guests {
		usage, observed, err := c.guestUsage(ctx, guest, peer)
		if err != nil {
			cpuQualified = false
			result.Errors = append(result.Errors, GuestUtilizationError{Guest: guest.Name, Err: err})
			continue
		}
		if observed == (GuestCPUObservation{}) {
			cpuQualified = false
		} else if cpuQualified {
			guestCPU = append(guestCPU, observed)
		}
		if !addGuestCounter(&result.CPUUsedMillis, usage.cpuMillis) ||
			!addGuestCounter(&result.RAMUsedMiB, usage.ramMiB) ||
			!addGuestCounter(&result.DiskUsedMiB, usage.diskMiB) {
			return UtilizationSample{}, ErrResponse
		}
		if usage.cpuKnown || usage.ramKnown || usage.diskMiB > 0 {
			result.Guests++
		}
		if !usage.cpuKnown || !usage.ramKnown || !usage.diskComplete {
			result.Errors = append(result.Errors, GuestUtilizationError{Guest: guest.Name, Err: ErrResponse})
		}
	}
	if cpuQualified && ctx.Err() == nil {
		result.GuestCPU = guestCPU
		result.guestCPUScope = guestCPUObservedScope{project: c.project, peer: peer}
	}
	return result, nil
}

type guestUsage struct {
	cpuMillis    int64
	ramMiB       int64
	diskMiB      int64
	cpuKnown     bool
	ramKnown     bool
	diskComplete bool
}

func addGuestCounter(sum *int64, value int64) bool {
	if value < 0 || value > math.MaxInt64-*sum {
		return false
	}
	*sum += value
	return true
}

func (c *Client) guestUsage(ctx context.Context, guest guestInventory, inventoryPeer string) (guestUsage, GuestCPUObservation, error) {
	var observed GuestCPUObservation
	if !name(guest.Name) {
		return guestUsage{}, observed, ErrConfiguration
	}
	bound := time.Now().UTC()
	response, err := c.request(ctx, http.MethodGet, "/1.0/instances/"+guest.Name+"/state", nil, nil)
	if err != nil {
		return guestUsage{}, observed, err
	}
	if response.kind != "sync" {
		return guestUsage{}, observed, ErrResponse
	}
	usage, err := parseGuestStateUsage(response.metadata, guest.measurableDisks)
	if err == nil && guest.container && inventoryPeer != "" && response.serverKeyFingerprint == inventoryPeer && usage.cpuKnown {
		observed = parseGuestCPUObservation(response.metadata, guest, bound)
	}
	return usage, observed, err
}

// Native 6.0/7.0 state can serialize zero both for a genuine zero and when
// stopped, unsupported, or unable to collect the counter. It also uses
// negative CPU sentinels, and can omit unsupported disk devices. Only
// positive values qualify. Positive sub-millisecond CPU may round to zero
// with known coverage; it is not the same as an ambiguous wire zero.
func parseGuestStateUsage(raw json.RawMessage, expectedDisks map[string]struct{}) (guestUsage, error) {
	fields, err := object(raw)
	if err != nil {
		return guestUsage{}, err
	}
	cpu, cpuKnown := positiveGuestCounter(fields["cpu"], "usage")
	memory, ramKnown := positiveGuestCounter(fields["memory"], "usage")
	disk, diskComplete := readGuestStateDisk(fields["disk"], expectedDisks)
	return guestUsage{
		cpuMillis: cpu / int64(time.Millisecond), cpuKnown: cpuKnown,
		ramMiB: bytesToMiB(memory), ramKnown: ramKnown, diskMiB: disk, diskComplete: diskComplete,
	}, nil
}

func positiveGuestCounter(raw json.RawMessage, key string) (int64, bool) {
	fields, err := object(raw)
	if err != nil {
		return 0, false
	}
	value, err := field[int64](fields, key)
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

func readGuestStateDisk(raw json.RawMessage, expected map[string]struct{}) (int64, bool) {
	data := bytes.TrimSpace(raw)
	if len(data) == 0 {
		return 0, false
	}
	switch data[0] {
	case '{':
		devices, err := object(data)
		if err != nil || len(devices) == 0 {
			return 0, false
		}
		complete := len(expected) > 0 && len(expected) == len(devices)
		var sum int64
		for device, counter := range devices {
			if _, matched := expected[device]; len(expected) != 0 && !matched {
				complete = false
				continue
			}
			value, known := positiveGuestCounter(counter, "usage")
			if !label(device) || !known {
				complete = false
				continue
			}
			if !addGuestCounter(&sum, value) {
				return 0, false
			}
		}
		return bytesToMiB(sum), complete
	case '[':
		// Retain the previous array-shaped compatibility input. This is
		// not the native Incus disk-state schema. It has no device keys
		// that could qualify coverage against native device inventory.
		var entries []json.RawMessage
		if json.Unmarshal(data, &entries) != nil || len(entries) == 0 {
			return 0, false
		}
		complete := true
		var mib int64
		for _, raw := range entries {
			entry, err := object(raw)
			if err != nil {
				complete = false
				continue
			}
			used, known := positiveGuestCounter(entry["usage"], "usage_bytes")
			if !known {
				complete = false
				continue
			}
			if !addGuestCounter(&mib, bytesToMiB(used)) {
				return 0, false
			}
		}
		// A partial native inventory cannot be satisfied by unnamed
		// array values; older fixtures have no native device inventory.
		return mib, complete && len(expected) == 0
	default:
		return 0, false
	}
}
