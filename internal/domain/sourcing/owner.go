package sourcing

import (
	"github.com/claudioed/order-sourcing/internal/domain/network"
)

// OwnerChoice picks whose stock ONE line takes at ONE node, given the listings
// of the line's catalogue item at that node (one per owner). A listing covers
// the line when it is not suppressed and its availableQty is at least quantity
// (this context applies no netting). The rule is deterministic and shared
// with delivery-promise (ADR 0012):
//   - operator-owned (ownerRef "") first, if it covers the line;
//   - otherwise the covering seller owner with the highest availableQty;
//   - ties go to the lowest ownerRef in byte order;
//   - no covering owner means the node is not a candidate for that line.
//
// A line never spans owners: quantities of different owners are never summed.
func OwnerChoice(quantity int, listings []network.Listing) (network.Listing, bool) {
	var best network.Listing
	found := false
	for _, l := range listings {
		if !l.CanShip(quantity) {
			continue
		}
		if !found || ownerBefore(l, best) {
			best, found = l, true
		}
	}
	return best, found
}

// ownerBefore reports whether covering listing a wins over covering listing b.
func ownerBefore(a, b network.Listing) bool {
	aOp, bOp := a.OwnerRef == network.OperatorOwned, b.OwnerRef == network.OperatorOwned
	if aOp != bOp {
		return aOp
	}
	if a.AvailableQty != b.AvailableQty {
		return a.AvailableQty > b.AvailableQty
	}
	if a.OwnerRef != b.OwnerRef {
		return a.OwnerRef < b.OwnerRef
	}
	return a.ID < b.ID
}
