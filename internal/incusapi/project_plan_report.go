package incusapi

import (
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
)

// Incus702ProjectPlan is input to an offline comparison, not a discovery or
// route record. Review digests must come from a separately reviewed source,
// never be inferred from the observed bodies. NetHostBudget is a modeled
// count/CPU/RAM/disk envelope after reserve, not current host availability.
type Incus702ProjectPlan struct {
	Project, StoragePool, ReviewedProjectSHA256 string
	ProjectResponse                             []byte
	Profiles                                    []Incus702PlanProfile
	Pools                                       []Incus702PlannedPool
	NetHostBudget                               *capacitybroker.Resources
	projectStateResponse                        []byte // only populated by the closed v2 capture decoder
}

type Incus702PlanProfile struct {
	Name, ReviewedSHA256 string
	Response             []byte
}

// Each logical pool is modeled at its complete proposed count, not as new
// demand added to existing charges. Size is the planned enrolled size in
// broker units; it must match the reviewed single profile in that model.
type Incus702PlannedPool struct {
	ID, Profile string
	Count       int64
	Size        capacitybroker.Resources
}

type Incus702PlanComparison uint8

const (
	Incus702PlanUnknown Incus702PlanComparison = iota
	Incus702PlanModeledFit
	Incus702PlanModeledExceedsBudget
)

// An arithmetic report about supplied inputs only. In particular, a planned
// mix can fit even if the declared project ceiling would permit more than the
// host budget. This report never establishes GARM routing, effective overrides,
// physical occupancy, runtime quota enforcement, or authority to allocate.
type Incus702ProjectPlanReport struct {
	AgainstProject, AgainstNetHost, ProjectCeilingAgainstNetHost Incus702PlanComparison
	SuppliedProjectState                                         Incus702ProjectStateCheck
}

// CompareIncus702ProjectPlan checks exact supplied v7.0.2 declarations against
// one proposed whole-project mix and one finite modeled host, using the
// stock planner for count/CPU/RAM/disk fit. It performs no I/O. No caller uses
// it for live planning; independent source/build/route/override/freshness
// qualification remains outside this offline comparison.
func CompareIncus702ProjectPlan(in Incus702ProjectPlan) Incus702ProjectPlanReport {
	const maxProfiles, maxPools, maxCount = 32, 32, 256
	if len(in.Profiles) == 0 || len(in.Profiles) > maxProfiles || len(in.Pools) == 0 || len(in.Pools) > maxPools || in.NetHostBudget == nil {
		return Incus702ProjectPlanReport{}
	}
	netHost := *in.NetHostBudget
	for _, n := range netHost {
		if n < 0 {
			return Incus702ProjectPlanReport{}
		}
	}
	profiles := make(map[string]capacitybroker.Resources, len(in.Profiles))
	var project incus702ProjectDeclaration
	for _, p := range in.Profiles {
		if _, duplicate := profiles[p.Name]; duplicate {
			return Incus702ProjectPlanReport{}
		}
		d, err := compareIncus702ProjectDeclaration(in.ProjectResponse, p.Response, incus702DeclarationReview{
			project: in.Project, pool: in.StoragePool, profile: p.Name,
			projectSHA256: in.ReviewedProjectSHA256, profileSHA256: p.ReviewedSHA256,
		})
		if err != nil {
			return Incus702ProjectPlanReport{}
		}
		profiles[p.Name] = capacitybroker.Resources{d.profileCPU * 1000, bytesToMiB(d.profileMemoryBytes), bytesToMiB(d.profileRootBytes), 1}
		project = d // Every row parsed the same pinned project declaration.
	}

	// Planner-only constants do not stand for an observed host or timestamp.
	const host, repo = "offline-model-host", "offline-model-repository"
	modelTime := time.Unix(1, 0)
	s := capacitybroker.Snapshot{
		Revision: "offline-project-plan", ObservedAt: modelTime, Complete: true,
		Hosts: map[string]capacitybroker.Host{}, Repositories: map[string]capacitybroker.Repository{},
		Pools: make(map[string]capacitybroker.Pool, len(in.Pools)),
	}
	used := make(map[string]bool, len(in.Profiles))
	var total int64
	for _, p := range in.Pools {
		size, ok := profiles[p.Profile]
		_, duplicate := s.Pools[p.ID]
		if !ok || !name(p.ID) || duplicate || p.Count < 0 || p.Count > maxCount-total || size != p.Size {
			return Incus702ProjectPlanReport{}
		}
		used[p.Profile] = true
		total += p.Count
		s.Pools[p.ID] = capacitybroker.Pool{
			Repository: repo, Size: size, Hosts: []string{host}, MaxRunners: p.Count, Waiting: p.Count, OldestWaiting: modelTime,
		}
	}
	if total == 0 || len(used) != len(profiles) {
		return Incus702ProjectPlanReport{}
	}
	s.Repositories[repo] = capacitybroker.Repository{Weight: 1, MaxRunners: total}
	maxRunners := min(project.instances, project.containers) // Explicit VM limit is zero.
	const mib = 1 << 20
	// Floor a byte ceiling before fitting any rounded-up per-runner sizes. For
	// comparing the ceiling itself to the host, round up so partial MiB cannot
	// conceal a larger configured ceiling.
	projectBudget := capacitybroker.Resources{project.cpu * 1000, project.memoryBytes / mib, project.diskBytes / mib, maxRunners}
	ceiling := capacitybroker.Resources{project.cpu * 1000, bytesToMiB(project.memoryBytes), bytesToMiB(project.diskBytes), maxRunners}

	compare := func(budget capacitybroker.Resources) Incus702PlanComparison {
		s.Global = budget
		s.Hosts[host] = capacitybroker.Host{Budget: budget, AcceptNew: true}
		plan, err := capacitybroker.Build(s, capacitybroker.Options{Now: modelTime, MaxAge: time.Second, MaxAssignments: int(total)})
		if err != nil {
			return Incus702PlanUnknown
		}
		if int64(len(plan.Assignments)) != total {
			return Incus702PlanModeledExceedsBudget
		}
		return Incus702PlanModeledFit
	}
	againstProject, againstHost := compare(projectBudget), compare(netHost)
	if againstProject == Incus702PlanUnknown || againstHost == Incus702PlanUnknown {
		return Incus702ProjectPlanReport{}
	}
	ceilingAgainstHost := Incus702PlanModeledFit
	for i, n := range ceiling {
		if n > netHost[i] {
			ceilingAgainstHost = Incus702PlanModeledExceedsBudget
			break
		}
	}
	out := Incus702ProjectPlanReport{
		AgainstProject: againstProject, AgainstNetHost: againstHost, ProjectCeilingAgainstNetHost: ceilingAgainstHost,
	}
	if in.projectStateResponse != nil {
		out.SuppliedProjectState = checkIncus702ProjectState(in.projectStateResponse, project)
	}
	return out
}
