package memory

import "github.com/claudioed/order-sourcing/internal/application/ports"

// The doubles implement the out ports.
var (
	_ ports.AssignmentRepository = (*AssignmentRepository)(nil)
	_ ports.ReadModels           = (*ReadModels)(nil)
	_ ports.WorldReader          = (*WorldReader)(nil)
	_ ports.EventOutbox          = (*Outbox)(nil)
	_ ports.Inbox                = (*Inbox)(nil)
	_ ports.UnitOfWork           = (*UnitOfWork)(nil)
	_ ports.Clock                = (*Clock)(nil)
	_ ports.IDGenerator          = (*SequentialIDs)(nil)
)
