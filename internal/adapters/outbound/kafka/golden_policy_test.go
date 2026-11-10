package kafka_test

import (
	"bytes"
	"testing"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/kafka"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

// TestReleaseOmitsThePolicyFieldsUnlessTheyApply pins the omission rules of
// ADR 0014: no policy published carries neither field; a policy without a
// surcharge carries policyVersion only; effectiveCostCents appears only when a
// surcharge changed the cost, even when the estimate itself is incomplete.
func TestReleaseOmitsThePolicyFieldsUnlessTheyApply(t *testing.T) {
	enc := kafka.Encoder{NewID: func() string { return "11111111-1111-4111-8111-111111111111" }}
	release := func(policyVersion int, estimate, effective *int64) []byte {
		return encodeIndented(t, enc, assignment.FulfillmentReleasedToNode{Base: base(), ReleaseNo: 1, OrderID: "ord-1", GroupNo: 1, NodeID: "n1",
			PickupDepartureID: "dep-1", RequiredShipBy: at, PromisedDeliverByLatest: at, Tier: assignment.TierTwoDay, Lines: lines,
			EstimatedCostCents: estimate, PolicyVersion: policyVersion, EffectiveCostCents: effective})
	}
	surcharged := int64(300)
	cases := []struct {
		name         string
		got          []byte
		wantPresent  []string
		wantAbsentIn []string
	}{
		{"no policy published", release(0, &complete, nil), nil, []string{"policyVersion", "effectiveCostCents"}},
		{"a policy without a surcharge", release(3, &complete, nil), []string{`"policyVersion": 3`}, []string{"effectiveCostCents"}},
		{"a surcharge on a complete estimate", release(3, &complete, &effective), []string{`"policyVersion": 3`, `"effectiveCostCents": 1315`}, nil},
		{"a surcharge on an incomplete estimate", release(3, nil, &surcharged), []string{`"effectiveCostCents": 300`}, []string{"estimatedCostCents"}},
	}
	for _, c := range cases {
		for _, want := range c.wantPresent {
			if !bytes.Contains(c.got, []byte(want)) {
				t.Errorf("%s: %s missing in\n%s", c.name, want, c.got)
			}
		}
		for _, absent := range c.wantAbsentIn {
			if bytes.Contains(c.got, []byte(`"`+absent+`"`)) {
				t.Errorf("%s: %s must be omitted in\n%s", c.name, absent, c.got)
			}
		}
	}
}
