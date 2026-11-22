package incusapi

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
)

// Incus702ProjectPlanCaptureMax bounds the entire offline JSON input.
const Incus702ProjectPlanCaptureMax = 3 << 20

// CheckIncus702ProjectPlanCapture decodes one closed offline capture. It never
// collects, modifies, sanitizes or authenticates data, derives review pins, or
// establishes route/build/freshness. Invalid inputs yield only an unknown report.
func CheckIncus702ProjectPlanCapture(raw []byte) Incus702ProjectPlanReport {
	unknown := Incus702ProjectPlanReport{}
	if len(raw) == 0 || len(raw) > Incus702ProjectPlanCaptureMax || !utf8.Valid(raw) {
		return unknown
	}
	header, err := object(raw)
	if err != nil {
		return unknown
	}
	schema, err := field[string](header, "schema")
	if err != nil || (schema != "incus-project-plan-capture/v1" && schema != "incus-project-plan-capture/v2" && schema != "incus-project-plan-capture/v3" && schema != "incus-project-plan-capture/v4") {
		return unknown
	}
	keys := []string{"schema", "project", "storage_pool", "reviewed_project_sha256", "project_response", "profiles", "pools", "net_host_budget"}
	hasState := schema == "incus-project-plan-capture/v2" || schema == "incus-project-plan-capture/v4"
	withoutDisk := schema == "incus-project-plan-capture/v3" || schema == "incus-project-plan-capture/v4"
	unknown.DiskNotEvaluated = withoutDisk
	readResources := incus702CaptureResources
	if withoutDisk {
		readResources = incus702CaptureResourcesWithoutDisk
	}
	if hasState {
		keys = append(keys, "project_state")
		unknown.SuppliedProjectState = Incus702ProjectStateUnknown
	}
	if len(header) != len(keys) {
		return unknown
	}
	for _, key := range keys {
		if _, ok := header[key]; !ok {
			return unknown
		}
	}
	fields := header
	project, b := field[string](fields, "project")
	storage, c := field[string](fields, "storage_pool")
	projectPin, d := field[string](fields, "reviewed_project_sha256")
	if b != nil || c != nil || d != nil ||
		!name(project) || !name(storage) || !fingerprint(projectPin) ||
		!incus702CaptureMetadataSanitized(fields["project_response"], "name", "config", "description", "used_by") {
		return unknown
	}
	profiles, a := field[[]json.RawMessage](fields, "profiles")
	pools, b := field[[]json.RawMessage](fields, "pools")
	budget, c := readResources(fields["net_host_budget"])
	if a != nil || b != nil || c != nil || len(profiles) < 1 || len(profiles) > 32 || len(pools) < 1 || len(pools) > 32 {
		return unknown
	}
	in := Incus702ProjectPlan{
		Project: project, StoragePool: storage, ReviewedProjectSHA256: projectPin,
		ProjectResponse: fields["project_response"], NetHostBudget: &budget, withoutDiskCeiling: withoutDisk,
	}
	if hasState {
		state, err := incus702DeclarationObject(fields["project_state"], "project", "response")
		sourceProject, labelErr := field[string](state, "project")
		if err != nil || labelErr != nil || sourceProject != project {
			return unknown
		}
		in.projectStateResponse = state["response"]
	}
	for _, raw := range profiles {
		p, err := incus702DeclarationObject(raw, "name", "reviewed_sha256", "response")
		if err != nil {
			return unknown
		}
		n, a := field[string](p, "name")
		pin, b := field[string](p, "reviewed_sha256")
		if a != nil || b != nil || !name(n) || !fingerprint(pin) ||
			!incus702CaptureMetadataSanitized(p["response"], "name", "project", "config", "devices", "description", "used_by") {
			return unknown
		}
		in.Profiles = append(in.Profiles, Incus702PlanProfile{Name: n, ReviewedSHA256: pin, Response: p["response"]})
	}
	for _, raw := range pools {
		p, err := incus702DeclarationObject(raw, "id", "profile", "count", "size")
		if err != nil {
			return unknown
		}
		id, a := field[string](p, "id")
		profile, b := field[string](p, "profile")
		count, c := field[int64](p, "count")
		size, d := readResources(p["size"])
		if a != nil || b != nil || c != nil || d != nil {
			return unknown
		}
		in.Pools = append(in.Pools, Incus702PlannedPool{ID: id, Profile: profile, Count: count, Size: size})
	}
	out := CompareIncus702ProjectPlan(in)
	if hasState && out.SuppliedProjectState == Incus702ProjectStateNotSupplied {
		return unknown
	}
	return out
}

func incus702CaptureResources(raw []byte) (capacitybroker.Resources, error) {
	var none capacitybroker.Resources
	fields, err := incus702DeclarationObject(raw, "cpu_millis", "ram_mib", "disk_mib", "runners")
	if err != nil {
		return none, err
	}
	cpu, a := field[int64](fields, "cpu_millis")
	ram, b := field[int64](fields, "ram_mib")
	disk, c := field[int64](fields, "disk_mib")
	runners, d := field[int64](fields, "runners")
	if a != nil || b != nil || c != nil || d != nil {
		return none, ErrResponse
	}
	return capacitybroker.Resources{cpu, ram, disk, runners}, nil
}

func incus702CaptureResourcesWithoutDisk(raw []byte) (capacitybroker.Resources, error) {
	fields, err := incus702DeclarationObject(raw, "cpu_millis", "ram_mib", "runners")
	if err != nil {
		return capacitybroker.Resources{}, err
	}
	cpu, a := field[int64](fields, "cpu_millis")
	ram, b := field[int64](fields, "ram_mib")
	runners, c := field[int64](fields, "runners")
	if a != nil || b != nil || c != nil {
		return capacitybroker.Resources{}, ErrResponse
	}
	return capacitybroker.Resources{cpu, ram, 0, runners}, nil
}

func incus702CaptureMetadataSanitized(raw []byte, names ...string) bool {
	fields, err := incus702DeclarationMetadata(raw, names...)
	if err != nil {
		return false
	}
	description, a := field[string](fields, "description")
	links, b := field[[]string](fields, "used_by")
	return a == nil && b == nil && description == "" && len(links) == 0
}
