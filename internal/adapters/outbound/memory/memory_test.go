package memory_test

import (
	"testing"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/memory"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/repocontract"
)

func TestMemoryKeepsThePersistenceContract(t *testing.T) {
	repocontract.Run(t, func(t *testing.T) repocontract.Fixture {
		s := memory.NewStore()
		return repocontract.Fixture{
			Assignments: s.Assignments(), ReadModels: s.ReadModels(), World: s.World(), Inbox: s.Inbox(),
			Outbox: s.Outbox(), UoW: s.UnitOfWork(),
			OutboxCount: func(*testing.T) int { return len(s.Outbox().Events()) },
		}
	})
}
