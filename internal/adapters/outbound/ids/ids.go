// Package ids is the adapter of ports.IDGenerator: "asg-<uuid v4>". Every id
// passes shared.ValidID (at most 64 characters from A-Z a-z 0-9 . _ : -).
package ids

import (
	"github.com/google/uuid"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

// UUID mints ids as "asg-<uuid v4>".
type UUID struct{}

var _ ports.IDGenerator = UUID{}

// NewAssignmentID implements ports.IDGenerator.
func (UUID) NewAssignmentID() assignment.ID { return assignment.ID("asg-" + uuid.NewString()) }
