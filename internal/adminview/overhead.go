package adminview

import "github.com/ShortCipher5/garm-capacity-controller/internal/admission"

// OverheadTotals are retained accounting subsets of the same overview snapshot.
// They are neither measured usage nor portable evidence for physical release.
type OverheadTotals struct {
	Domain   admission.Resources `json:"domain"`
	Artifact admission.Resources `json:"artifact"`
}

func overheadTotals(a admission.Allocations) (OverheadTotals, error) {
	d, r, g := a.DomainOverhead, a.ArtifactOverhead, a.Global
	if d.Runners != 0 || r.Runners != 0 || r.CPU != 0 || r.RAM != 0 {
		return OverheadTotals{}, ErrConfiguration
	}
	domains := []int64{d.CPU, d.RAM, d.Disk, d.Runners}
	artifacts := []int64{r.CPU, r.RAM, r.Disk, r.Runners}
	global := []int64{g.CPU, g.RAM, g.Disk, g.Runners}
	for i, n := range domains {
		if n < 0 || artifacts[i] < 0 || global[i] < n || artifacts[i] > global[i]-n {
			return OverheadTotals{}, ErrConfiguration
		}
	}
	return OverheadTotals{Domain: d, Artifact: r}, nil
}
