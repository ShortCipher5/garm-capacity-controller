//go:build !linux

package cihealthsource

// Owned history is Linux-only. Timing reads on other platforms keep their
// existing interface and do not mint a persistence link.
type cycleSourceInstance struct{}

func captureCycleSource(Source) cycleSourceInstance { return cycleSourceInstance{} }
