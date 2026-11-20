//go:build !linux

package cihealthsource

import (
	"context"

	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

func (cycleSourceInstance) prepareWorkflowRunLink(context.Context, demandrepair.CIAttempt) demandrepair.WorkflowRunLink {
	return demandrepair.WorkflowRunLink{}
}
