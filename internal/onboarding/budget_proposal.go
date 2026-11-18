package onboarding

// BudgetProposal is a versioned advisory calculation over one discovery view.
// Omitted dimensions are unknown, never unlimited. It grants no admission or
// provisioning authority and does not save runner configurations.
type BudgetProposal struct {
	Version    int              `json:"version"`
	Budget     map[string]int64 `json:"budget"`
	Reserve    map[string]int64 `json:"reserve"`
	Warnings   []string         `json:"warnings"`
	Sizes      []PlanningSize   `json:"sizes"`
	SizeSource string           `json:"size_source"`
}

type PlanningSize struct {
	Name string `json:"name"`
	CPU  int64  `json:"cpu_millis"`
	RAM  int64  `json:"ram_mib"`
	Disk int64  `json:"disk_mib"`
}

func DefaultPlanningSizes() []PlanningSize {
	return []PlanningSize{{"small", 1000, 2048, 20480}, {"medium", 2000, 4096, 40960}, {"large", 4000, 8192, 81920}}
}

const maxPlanningMagnitude int64 = 9007199254740991

func planningKnown(v *int64) bool                 { return v != nil && *v >= 0 && *v <= maxPlanningMagnitude }
func planningHeadroom(total, minimum int64) int64 { return min(total, max(minimum, (total+9)/10)) }

// ProposeBudget subtracts OS headroom and existing guest commitments, including
// attributed guests. Disk reserves used space PLUS full guest limits because
// this observation has no trustworthy attribution of used bytes to each guest.
// The deliberate overlap is conservative. Other projects/services remain out
// of scope, so even a complete proposal is not qualified free capacity.
func ProposeBudget(result DiscoveryResult) BudgetProposal {
	p := BudgetProposal{Version: 1, Budget: map[string]int64{}, Reserve: map[string]int64{}, Warnings: []string{}, Sizes: DefaultPlanningSizes(), SizeSource: "illustrative-presets"}
	totals := result.Totals
	if totals == nil {
		totals = &DiscoveryTotals{}
	}
	inventoryKnown := true
	for _, blocker := range result.Blockers {
		if blocker == "guest_inventory_unavailable" {
			inventoryKnown = false
		}
	}
	for _, dim := range []struct {
		key     string
		total   *int64
		minimum int64
		guest   func(GuestView) *int64
	}{
		{"cpu_millis", totals.CPUMillis, 1000, func(g GuestView) *int64 { return g.CPUMillis }},
		{"ram_mib", totals.RAMMiB, 2048, func(g GuestView) *int64 { return g.RAMMiB }},
		{"disk_mib", totals.DiskMiB, 10240, func(g GuestView) *int64 { return g.DiskMiBRoot }},
	} {
		known := inventoryKnown && planningKnown(dim.total) && *dim.total > 0
		var guests int64
		for _, g := range result.Guests {
			v := dim.guest(g)
			if !planningKnown(v) {
				known = false
				break
			}
			if guests > maxPlanningMagnitude-*v {
				known = false
				break
			}
			guests += *v
		}
		if !known {
			p.Warnings = append(p.Warnings, dim.key+": usable budget unknown because totals or existing guest limits are incomplete or out of range.")
			continue
		}
		total := *dim.total
		reserve := planningHeadroom(total, dim.minimum)
		p.Reserve[dim.key] = reserve
		budget := max(int64(0), total-reserve-guests)
		if dim.key == "disk_mib" {
			if !planningKnown(totals.DiskMiBUsed) {
				p.Warnings = append(p.Warnings, "disk_mib: disk usage unknown; enter a reviewed disk budget manually.")
				continue
			}
			budget = max(int64(0), budget-*totals.DiskMiBUsed)
			if totals.StoragePool != "" {
				if !planningKnown(totals.PoolDiskMiB) || !planningKnown(totals.PoolDiskUsed) || *totals.PoolDiskMiB == 0 {
					p.Warnings = append(p.Warnings, "disk_mib: selected storage pool capacity or usage is unknown.")
					continue
				}
				poolFree := max(int64(0), *totals.PoolDiskMiB-*totals.PoolDiskUsed-guests-planningHeadroom(*totals.PoolDiskMiB, 10240))
				budget = min(budget, poolFree)
			}
			if budget == 0 {
				p.Warnings = append(p.Warnings, "No disk capacity remains in this proposal. Disk budget is left blank because zero would mean unconstrained, not no capacity.")
				continue
			}
		}
		p.Budget[dim.key] = budget
	}
	p.Warnings = append(p.Warnings, "Existing guest limits are reserved conservatively, including attributed guests. Other projects and host services may be missing; this is a proposal, not qualified free capacity.")
	return p
}
