package incusapi

// Incus702ProjectStateCheck classifies only the supplied state's configured
// caps and declared allocations. The v7.0.2 body has no project identifier.
// A matching operator label is not an independently verified source.
type Incus702ProjectStateCheck uint8

const (
	Incus702ProjectStateNotSupplied Incus702ProjectStateCheck = iota
	Incus702ProjectStateUnknown
	Incus702ProjectStateConflicting
	Incus702ProjectStateReportedAllocationsNonzero
	Incus702ProjectStateReportedAllocationsZero
)

// The native state reports DB allocations, not usage from running cgroups or
// actual filesystem usage. For aggregate values Incus skips some unset inputs.
// A valid report therefore never lends present or future-capacity credit.
//
// https://github.com/lxc/incus/blob/v7.0.2/internal/server/project/state.go#L14-L91
// https://github.com/lxc/incus/blob/v7.0.2/shared/api/project.go#L81-L101
func checkIncus702ProjectState(raw []byte, declaration incus702ProjectDeclaration, withoutDisk bool) Incus702ProjectStateCheck {
	data, err := incus702SyncMetadata(raw, "resources")
	if err != nil {
		return Incus702ProjectStateUnknown
	}
	const instances, containers, vms = "instances", "containers", "virtual-machines"
	expected := map[string]int64{
		instances: declaration.instances, containers: declaration.containers, vms: 0,
		"cpu": declaration.cpu, "memory": declaration.memoryBytes, "disk": declaration.diskBytes,
	}
	if withoutDisk {
		// Unset is -1 in the native state. That says nothing about storage-pool
		// defaults, backing filesystem occupancy, or effective volume size.
		expected["disk"] = -1
	}
	const networks, processes = "networks", "processes"
	resources, err := incus702DeclarationObject(data["resources"], instances, containers, vms, "cpu", "memory", "disk", networks, processes)
	if err != nil {
		return Incus702ProjectStateUnknown
	}
	usage := make(map[string]int64, len(resources))
	conflicts := false
	for metric, raw := range resources {
		// Exact v7.0.2 Go wire case: fields have no lowercase JSON tags.
		value, err := incus702DeclarationObject(raw, "Limit", "Usage")
		if err != nil {
			return Incus702ProjectStateUnknown
		}
		limit, a := field[int64](value, "Limit")
		count, b := field[int64](value, "Usage")
		if a != nil || b != nil || limit < -1 || count < 0 {
			return Incus702ProjectStateUnknown
		}
		usage[metric] = count
		if limit >= 0 && count > limit {
			conflicts = true
		}
		if want, required := expected[metric]; required && limit != want {
			conflicts = true
		}
	}
	// VMs are excluded. Never treat counts that disagree as an empty baseline.
	// CPU, memory and process allocations are instance aggregates. Storage
	// (custom volumes) and networks can exist without any instances.
	if conflicts || usage[vms] != 0 || usage[instances] != usage[containers] ||
		(usage[instances] == 0 && (usage["cpu"] != 0 || usage["memory"] != 0 || usage[processes] != 0)) ||
		(usage[instances] > 0 && (usage["cpu"] < usage[instances] || usage["memory"] < usage[instances] || (!withoutDisk && usage["disk"] < usage[instances]))) {
		return Incus702ProjectStateConflicting
	}
	if usage[instances] != 0 || (!withoutDisk && usage["disk"] != 0) {
		return Incus702ProjectStateReportedAllocationsNonzero
	}
	return Incus702ProjectStateReportedAllocationsZero
}
