package ids_test

import (
	"regexp"
	"testing"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/ids"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

const uuidV4 = `[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`

func TestUUIDMintsPrefixedValidDistinctIDs(t *testing.T) {
	g := ids.UUID{}
	first, second := string(g.NewAssignmentID()), string(g.NewAssignmentID())
	if !regexp.MustCompile(`^asg-` + uuidV4 + `$`).MatchString(first) {
		t.Fatalf("id = %q, want asg-<uuid v4>", first)
	}
	if !shared.ValidID(first) {
		t.Fatalf("id %q is not a valid aggregate id", first)
	}
	if first == second {
		t.Fatalf("two ids are both %q", first)
	}
}
