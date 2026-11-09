package sourcing

import (
	"testing"

	"github.com/claudioed/order-sourcing/internal/domain/network"
)

func owned(id, owner string, qty int) network.Listing {
	return network.Listing{ID: id, CatalogItemID: "cat-1", NodeID: "fc-a", OwnerRef: owner, AvailableQty: qty}
}

func suppressed(l network.Listing) network.Listing {
	l.Suppressed = true
	return l
}

// ownerCases is the OwnerChoice table (ADR 0012), a slice so the order is fixed.
var ownerCases = []struct {
	name     string
	quantity int
	listings []network.Listing
	want     string // the chosen listing id; "" = no covering owner
}{
	{"no listing", 1, nil, ""},
	{"operator alone covers", 3, []network.Listing{owned("l-op", "", 3)}, "l-op"},
	{"operator alone short", 4, []network.Listing{owned("l-op", "", 3)}, ""},
	{"operator first even when a seller has more", 2, []network.Listing{owned("l-s", "sel-a", 9), owned("l-op", "", 2)}, "l-op"},
	{"operator short falls to the covering seller", 5, []network.Listing{owned("l-op", "", 4), owned("l-s", "sel-a", 5)}, "l-s"},
	{"suppressed operator falls to the seller", 1, []network.Listing{suppressed(owned("l-op", "", 9)), owned("l-s", "sel-a", 1)}, "l-s"},
	{"highest seller quantity wins", 2, []network.Listing{owned("l-a", "sel-a", 3), owned("l-b", "sel-b", 7), owned("l-c", "sel-c", 5)}, "l-b"},
	{"highest seller quantity wins in any order", 2, []network.Listing{owned("l-b", "sel-b", 7), owned("l-a", "sel-a", 3)}, "l-b"},
	{"a short seller never wins on quantity", 6, []network.Listing{owned("l-a", "sel-a", 6), owned("l-b", "sel-b", 5)}, "l-a"},
	{"a suppressed seller never wins", 1, []network.Listing{suppressed(owned("l-b", "sel-b", 9)), owned("l-a", "sel-a", 2)}, "l-a"},
	{"tie goes to the lowest ownerRef", 2, []network.Listing{owned("l-b", "sel-b", 4), owned("l-a", "sel-a", 4)}, "l-a"},
	{"tie in byte order, upper case first", 2, []network.Listing{owned("l-low", "sel-a", 4), owned("l-up", "SEL-Z", 4)}, "l-up"},
	{"tie stays stable when the lowest comes first", 2, []network.Listing{owned("l-a", "sel-a", 4), owned("l-b", "sel-b", 4)}, "l-a"},
	{"one owner twice falls to the lower listing id", 2, []network.Listing{owned("l-2", "sel-a", 4), owned("l-1", "sel-a", 4)}, "l-1"},
	{"owners are never summed", 5, []network.Listing{owned("l-a", "sel-a", 3), owned("l-b", "sel-b", 3)}, ""},
	{"operator and seller are never summed", 5, []network.Listing{owned("l-op", "", 3), owned("l-b", "sel-b", 3)}, ""},
}

func TestOwnerChoice(t *testing.T) {
	for _, c := range ownerCases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := OwnerChoice(c.quantity, c.listings)
			if ok != (c.want != "") || got.ID != c.want {
				t.Fatalf("OwnerChoice(%d) = %q ok=%v, want %q", c.quantity, got.ID, ok, c.want)
			}
		})
	}
}

// TestNodeIsNotACandidateWhenNoOwnerCoversTheLine: two owners with 3 units each
// at one node, a line of 5 — the node is not a candidate (ADR 0012).
func TestNodeIsNotACandidateWhenNoOwnerCoversTheLine(t *testing.T) {
	w := world()
	w.Listings = []network.Listing{
		{ID: "lst-op", CatalogItemID: "cat-1", NodeID: "fc-a", AvailableQty: 3},
		{ID: "lst-s1", CatalogItemID: "cat-1", NodeID: "fc-a", OwnerRef: "sel-1", AvailableQty: 3},
		listing("fc-b", "cat-1", 10),
	}
	r := request()
	r.Lines[0].Quantity = 5
	wantNodes(t, Search(w, r), "fc-b")
	w.Listings = w.Listings[:2]
	if _, ok := Best(w, r); ok {
		t.Fatal("no node covers the line from one owner: the candidate set is empty")
	}
}

func TestResolveLinesCarriesTheChosenOwner(t *testing.T) {
	w := world()
	w.Listings = []network.Listing{
		{ID: "lst-op", CatalogItemID: "cat-1", NodeID: "fc-a", AvailableQty: 1},
		{ID: "lst-s2", CatalogItemID: "cat-1", NodeID: "fc-a", OwnerRef: "sel-2", AvailableQty: 4},
		{ID: "lst-s1", CatalogItemID: "cat-1", NodeID: "fc-a", OwnerRef: "sel-1", AvailableQty: 4},
		{ID: "lst-other", CatalogItemID: "cat-1", NodeID: "fc-b", OwnerRef: "sel-0", AvailableQty: 9},
		{ID: "lst-item", CatalogItemID: "cat-9", NodeID: "fc-a", OwnerRef: "sel-0", AvailableQty: 9},
	}
	r := request()
	got, ok := ResolveLines(w, "fc-a", r.Lines)
	if !ok || got[0].ListingID != "lst-s1" || got[0].OwnerRef != "sel-1" || r.Lines[0].OwnerRef != "" {
		t.Fatalf("lines = %+v ok=%v (input must not change)", got, ok)
	}
	r.Lines[0].Quantity = 1
	got, ok = ResolveLines(w, "fc-a", r.Lines)
	if !ok || got[0].ListingID != "lst-op" || got[0].OwnerRef != "" {
		t.Fatalf("operator stock covers the line first: %+v ok=%v", got, ok)
	}
}
