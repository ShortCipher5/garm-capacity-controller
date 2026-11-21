package incusapi

import (
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
)

func incus702PlanFixture() Incus702ProjectPlan {
	r := declarationReviewFixture()
	heavy := strings.NewReplacer(
		"fixture-small", "fixture-heavy", `"limits.cpu":"2"`, `"limits.cpu":"4"`,
		`"limits.memory":"2GiB"`, `"limits.memory":"8GiB"`, `"size":"12GiB"`, `"size":"25GiB"`,
	).Replace(profileDeclarationBody)
	net := capacitybroker.Resources{12000, 24 * 1024, 120 * 1024, 5}
	return Incus702ProjectPlan{
		Project: r.project, StoragePool: r.pool, ReviewedProjectSHA256: r.projectSHA256, ProjectResponse: []byte(projectDeclarationBody),
		Profiles: []Incus702PlanProfile{
			{r.profile, r.profileSHA256, []byte(profileDeclarationBody)},
			{"fixture-heavy", "436f11ee635f5f33b275c27a354bde7065b15b8f380b19289c38522bb0619ab6", []byte(heavy)},
		},
		Pools: []Incus702PlannedPool{
			{"repo-a", r.profile, 2, capacitybroker.Resources{2000, 2 * 1024, 12 * 1024, 1}},
			{"repo-b", "fixture-heavy", 1, capacitybroker.Resources{4000, 8 * 1024, 25 * 1024, 1}},
		},
		NetHostBudget: &net,
	}
}

func TestIncus702ProjectPlanReportUsesCheckedProfiles(t *testing.T) {
	in := incus702PlanFixture()
	fit, over := Incus702PlanModeledFit, Incus702PlanModeledExceedsBudget
	if got := CompareIncus702ProjectPlan(in); got != (Incus702ProjectPlanReport{AgainstProject: fit, AgainstNetHost: fit, ProjectCeilingAgainstNetHost: fit}) {
		t.Fatalf("expected separate modeled fits, got %+v", got)
	}
	for dimension, name := range []string{"cpu", "ram", "disk", "count"} {
		t.Run(name, func(t *testing.T) {
			in := incus702PlanFixture()
			budget := capacitybroker.Resources{8000, 12 * 1024, 49 * 1024, 3}
			in.NetHostBudget = &budget
			if got := CompareIncus702ProjectPlan(in); got != (Incus702ProjectPlanReport{AgainstProject: fit, AgainstNetHost: fit, ProjectCeilingAgainstNetHost: over}) {
				t.Fatalf("exact mix and full declared ceiling are distinct, got %+v", got)
			}
			budget[dimension]--
			if got := CompareIncus702ProjectPlan(in); got != (Incus702ProjectPlanReport{AgainstProject: fit, AgainstNetHost: over, ProjectCeilingAgainstNetHost: over}) {
				t.Fatalf("one short host dimension must refuse modeled fit, got %+v", got)
			}
		})
	}
	// The declared CPU ceiling rejects this five-slot mix independently of the host.
	in.Pools[0].Count, in.Pools[1].Count = 3, 2
	in.NetHostBudget = &capacitybroker.Resources{16000, 24 * 1024, 120 * 1024, 5}
	if got := CompareIncus702ProjectPlan(in); got != (Incus702ProjectPlanReport{AgainstProject: over, AgainstNetHost: fit, ProjectCeilingAgainstNetHost: fit}) {
		t.Fatalf("host room cannot hide the project's aggregate CPU limit, got %+v", got)
	}
}

func TestIncus702ProjectPlanReportSharesContainerCount(t *testing.T) {
	in := incus702PlanFixture()
	in.Profiles = in.Profiles[:1]
	in.Pools[1].Profile, in.Pools[1].Size = in.Pools[0].Profile, in.Pools[0].Size
	in.Pools[0].Count, in.Pools[1].Count = 3, 3
	in.NetHostBudget = &capacitybroker.Resources{12000, 24 * 1024, 120 * 1024, 6}
	fit, over := Incus702PlanModeledFit, Incus702PlanModeledExceedsBudget
	// Each pool fits alone and the total instance limit is six; only the
	// shared project container limit of five is exceeded.
	if got := CompareIncus702ProjectPlan(in); got != (Incus702ProjectPlanReport{AgainstProject: over, AgainstNetHost: fit, ProjectCeilingAgainstNetHost: fit}) {
		t.Fatalf("profile reuse must not multiply the container cap: %+v", got)
	}
}

func TestIncus702ProjectPlanReportUnknownOnDrift(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Incus702ProjectPlan)
	}{
		{"missing-budget", func(p *Incus702ProjectPlan) { p.NetHostBudget = nil }},
		{"negative-budget", func(p *Incus702ProjectPlan) { p.NetHostBudget[2] = -1 }},
		{"missing-profile", func(p *Incus702ProjectPlan) { p.Profiles = p.Profiles[:1] }},
		{"profile-owner", func(p *Incus702ProjectPlan) {
			p.Profiles[0].Response = []byte(strings.Replace(profileDeclarationBody, `"project":"fixture-runners"`, `"project":"default"`, 1))
		}},
		{"profile-hash", func(p *Incus702ProjectPlan) { p.Profiles[0].ReviewedSHA256 = p.Profiles[1].ReviewedSHA256 }},
		{"unplanned-extra-profile", func(p *Incus702ProjectPlan) { p.Pools = p.Pools[:1] }},
		{"duplicate-profile", func(p *Incus702ProjectPlan) { p.Profiles[1] = p.Profiles[0] }},
		{"duplicate-pool", func(p *Incus702ProjectPlan) { p.Pools[1].ID = p.Pools[0].ID }},
		{"planned-size-mismatch", func(p *Incus702ProjectPlan) { p.Pools[1].Size[2]-- }},
		{"negative-count", func(p *Incus702ProjectPlan) { p.Pools[0].Count = -1 }},
		{"oversize-mix", func(p *Incus702ProjectPlan) { p.Pools[0].Count = 256 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := incus702PlanFixture()
			tc.edit(&in)
			if got := CompareIncus702ProjectPlan(in); got != (Incus702ProjectPlanReport{}) {
				t.Fatalf("an incomplete or mismatched declaration must not return numeric fit: %+v", got)
			}
		})
	}
}
