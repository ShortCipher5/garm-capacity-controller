// capacity-incus-project-plan compares an offline operator capture. It takes
// no addresses, credentials, filenames, daemon commands or effect options.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

const usage = "usage: capacity-incus-project-plan < sanitized-capture.json"

type output struct {
	Schema                  string `json:"schema"`
	PlanAgainstProject      string `json:"plan_against_project"`
	PlanAgainstNetHost      string `json:"plan_against_net_host"`
	ProjectCeilingVsNetHost string `json:"project_ceiling_against_net_host"`
	RouteVerified           bool   `json:"route_verified"`
	EnforcementVerified     bool   `json:"enforcement_verified"`
	SuppliedProjectState    string `json:"supplied_project_state,omitempty"`
	DiskAssessment          string `json:"disk_assessment,omitempty"`
}

func comparison(code incusapi.Incus702PlanComparison) string {
	switch code {
	case incusapi.Incus702PlanModeledFit:
		return "modeled_fit"
	case incusapi.Incus702PlanModeledExceedsBudget:
		return "modeled_exceeds_budget"
	default:
		return "unknown"
	}
}

func run(args []string, input io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	report := incusapi.Incus702ProjectPlanReport{}
	if input != nil {
		const max = incusapi.Incus702ProjectPlanCaptureMax
		raw, err := io.ReadAll(io.LimitReader(input, max+1))
		if err == nil && len(raw) <= max {
			report = incusapi.CheckIncus702ProjectPlanCapture(raw)
		}
		clear(raw)
	}
	out := output{
		Schema:                  "incus-project-plan-report/v1",
		PlanAgainstProject:      comparison(report.AgainstProject),
		PlanAgainstNetHost:      comparison(report.AgainstNetHost),
		ProjectCeilingVsNetHost: comparison(report.ProjectCeilingAgainstNetHost),
	}
	if report.DiskNotEvaluated {
		out.Schema = "incus-project-plan-report/v3"
		out.DiskAssessment = "not_evaluated"
	}
	if report.SuppliedProjectState != incusapi.Incus702ProjectStateNotSupplied {
		out.Schema = "incus-project-plan-report/v2"
		if report.DiskNotEvaluated {
			out.Schema = "incus-project-plan-report/v4"
		}
		switch report.SuppliedProjectState {
		case incusapi.Incus702ProjectStateConflicting:
			out.SuppliedProjectState = "conflicting"
		case incusapi.Incus702ProjectStateReportedAllocationsNonzero:
			out.SuppliedProjectState = "reported_allocations_nonzero"
			if report.DiskNotEvaluated {
				out.SuppliedProjectState = "reported_count_cpu_ram_allocations_nonzero"
			}
		case incusapi.Incus702ProjectStateReportedAllocationsZero:
			out.SuppliedProjectState = "reported_allocations_zero"
			if report.DiskNotEvaluated {
				out.SuppliedProjectState = "reported_count_cpu_ram_allocations_zero"
			}
		default:
			out.SuppliedProjectState = "unknown"
		}
	}
	if json.NewEncoder(stdout).Encode(out) != nil {
		fmt.Fprintln(stderr, "could not write offline comparison report")
		return 2
	}
	parts := [...]string{out.PlanAgainstProject, out.PlanAgainstNetHost, out.ProjectCeilingVsNetHost}
	for _, part := range parts {
		if part == "unknown" {
			return 2
		}
	}
	if out.SuppliedProjectState == "unknown" {
		return 2
	}
	if report.SuppliedProjectState == incusapi.Incus702ProjectStateConflicting || report.SuppliedProjectState == incusapi.Incus702ProjectStateReportedAllocationsNonzero {
		return 1
	}
	for _, part := range parts {
		if part != "modeled_fit" {
			return 1
		}
	}
	return 0
}

func main() {
	// No terminal prompts, FIFO, socket or stream input. Use a finite local
	// regular file opened by the operator, not by this command.
	var in io.Reader
	if info, err := os.Stdin.Stat(); err == nil && info.Mode().IsRegular() && info.Size() <= incusapi.Incus702ProjectPlanCaptureMax {
		in = os.Stdin
	}
	os.Exit(run(os.Args[1:], in, os.Stdout, os.Stderr))
}
