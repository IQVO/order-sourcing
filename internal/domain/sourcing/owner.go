package sourcing

import (
	"cmp"
	"slices"
	"strings"

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
	covering := slices.DeleteFunc(slices.Clone(listings), func(l network.Listing) bool { return !l.CanShip(quantity) })
	if len(covering) == 0 {
		return network.Listing{}, false
	}
	return slices.MinFunc(covering, compareOwners), true
}

// compareOwners orders two covering listings by the OwnerChoice rule: the
// operator first, then the higher availableQty, then the lower ownerRef in
// byte order; the listing id only separates malformed duplicates of one owner.
func compareOwners(a, b network.Listing) int {
	return cmp.Or(
		boolRank(a.OwnerRef == network.OperatorOwned, b.OwnerRef == network.OperatorOwned),
		cmp.Compare(b.AvailableQty, a.AvailableQty),
		strings.Compare(a.OwnerRef, b.OwnerRef),
		strings.Compare(a.ID, b.ID),
	)
}
