package kafka_test

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/claudioed/order-sourcing/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/kafka"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

var at = time.Date(2026, 10, 22, 14, 10, 0, 0, time.UTC)

func base() assignment.Base {
	return assignment.Base{Header: shared.Header{Version: 3, At: at}, AssignmentID: "asg-7f3c2d10"}
}

var lines = []assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", ListingID: "lst-1", Quantity: 2}}

func allEvents() []shared.Event {
	return []shared.Event{
		assignment.FulfillmentQueued{Base: base(), OrderID: "ord-1", GroupNo: 1, PromiseID: "prm-1", DeliverByLatest: at, Lines: lines},
		assignment.FulfillmentReleasedToNode{Base: base(), ReleaseNo: 1, OrderID: "ord-1", GroupNo: 1, NodeID: "n1", PickupDepartureID: "dep-1",
			RequiredShipBy: at, PromisedDeliverByLatest: at, Tier: assignment.TierTwoDay, Lines: lines},
		assignment.FulfillmentReassigned{Base: base(), FromNodeID: "n1", ToNodeID: "n2", Reason: "REJECTED"},
		assignment.FulfillmentLocked{Base: base(), OrderID: "ord-1", GroupNo: 1, PromiseID: "prm-1", NodeID: "n1", PickupDepartureID: "dep-1", LockedAt: at, LockReason: "ACCEPTED"},
		assignment.FulfillmentShipped{Base: base(), OrderID: "ord-1", GroupNo: 1, NodeID: "n1", TrackingNumbers: []string{"TRK1"}, ShippedAt: at},
		assignment.FulfillmentDelivered{Base: base(), OrderID: "ord-1", GroupNo: 1, DeliveredAt: at},
		assignment.FulfillmentUnsourceable{Base: base(), OrderID: "ord-1", GroupNo: 1, PromiseID: "prm-1", ExcludedNodes: []assignment.ExcludedNode{{NodeID: "n1", Reason: "REJECTED"}}, Reason: "NO_CANDIDATE"},
		assignment.FulfillmentCancelled{Base: base(), OrderID: "ord-1", GroupNo: 1, Reason: "CUSTOMER_REQUEST"},
		assignment.FulfillmentCancelRefused{Base: base(), OrderID: "ord-1", GroupNo: 1, Reason: "LOCKED"},
		assignment.FulfillmentConstraintRevised{Base: base(), RevisionNo: 1, DeliverByLatest: at},
	}
}

// publishedKeys reads, per published message of apis/asyncapi.yaml, the keys of the example `data`.
func publishedKeys(t *testing.T) map[string][]string {
	t.Helper()
	raw, err := os.ReadFile("../../../../apis/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Channels   map[string]struct{ Messages map[string]any }
		Components struct {
			Messages map[string]struct {
				Examples []struct{ Payload struct{ Data map[string]any } }
			}
		}
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, ch := range []string{"orderSourcingEvents", "orderSourcingAnalytics"} {
		for msg := range doc.Channels[ch].Messages {
			for k := range doc.Components.Messages[msg].Examples[0].Payload.Data {
				out[msg] = append(out[msg], k)
			}
			sort.Strings(out[msg])
		}
	}
	return out
}

func TestEncoderMatchesThePublishedContract(t *testing.T) {
	want := publishedKeys(t)
	if len(want) != 10 {
		t.Fatalf("the contract publishes %d messages, want 10", len(want))
	}
	enc := kafka.Encoder{NewID: func() string { return "11111111-1111-4111-8111-111111111111" }}
	for _, ev := range allEvents() {
		m, err := enc.Encode(ev)
		if err != nil {
			t.Fatalf("%s: %v", ev.EventName(), err)
		}
		topic := kafka.TopicEvents
		if ev.Stream() == shared.StreamAnalytics {
			topic = kafka.TopicAnalytics
		}
		if m.Topic != topic || string(m.Key) != "asg-7f3c2d10" || m.Subject != "asg-7f3c2d10" || m.EventID == "" || len(m.Headers) != 1 {
			t.Errorf("%s: envelope %+v", ev.EventName(), m)
		}
		if m.EventType != "com.retailnetwork.commerce.order-sourcing.assignment."+ev.EventName() {
			t.Errorf("%s: type %s", ev.EventName(), m.EventType)
		}
		e, err := cloudevents.Decode(m.Value)
		if err != nil {
			t.Fatalf("%s: %v", ev.EventName(), err)
		}
		var data map[string]any
		if err := json.Unmarshal(e.Data(), &data); err != nil {
			t.Fatal(err)
		}
		var got []string
		for k := range data {
			got = append(got, k)
		}
		sort.Strings(got)
		if g, w := join(got), join(want[ev.EventName()]); g != w {
			t.Errorf("%s data keys\n got  %s\n want %s", ev.EventName(), g, w)
		}
	}
}

func join(s []string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestEncoderMintsADistinctIDPerCallAndRejectsForeignEvents(t *testing.T) {
	enc := kafka.Encoder{}
	a, err := enc.Encode(allEvents()[0])
	b, _ := enc.Encode(allEvents()[0])
	if err != nil || a.EventID == b.EventID {
		t.Fatalf("each encode mints its own id: %v %s %s", err, a.EventID, b.EventID)
	}
	if _, err := enc.Encode(foreign{}); err == nil {
		t.Fatal("an event with no wire form must be refused")
	}
}

type foreign struct{ assignment.Base }

func (foreign) EventName() string { return "Nope" }
