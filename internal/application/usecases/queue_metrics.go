package usecases

import (
	"context"
	"errors"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/views"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// failureReasons maps a QueueAssignment failure to a stable slug (the same
// vocabulary as the REST problem types where one exists), in match order.
var failureReasons = []struct {
	err    error
	reason string
}{
	{ports.ErrConcurrentUpdate, "concurrent-update"},
	{ports.ErrDuplicateGroup, "duplicate-group"},
	{shared.ErrValidation, "validation-failed"},
}

// internalErrorReason is the reason of every failure that is not one of the
// above (a database error, a bug).
const internalErrorReason = "internal-error"

// recordOutcome counts the result of one QueueAssignment: one outcome per
// shipment group it created, or one failure. The command ran in a unit of work
// that is already committed or rolled back, so a rolled-back attempt never
// counts a group. A replayed event (nothing created) records nothing.
func (uc *QueueAssignment) recordOutcome(ctx context.Context, created []views.Assignment, err error) {
	if err != nil {
		uc.metrics().SourcingFailed(ctx, failureReason(err))
		return
	}
	for _, a := range created {
		uc.metrics().GroupSourced(ctx, a.State)
	}
}

func failureReason(err error) string {
	for _, r := range failureReasons {
		if errors.Is(err, r.err) {
			return r.reason
		}
	}
	return internalErrorReason
}
