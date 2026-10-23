package kafka_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	consumer "github.com/claudioed/order-sourcing/internal/adapters/inbound/kafka"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/memory"
	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fixture struct {
	store *memory.Store
	h     consumer.Handlers
}

func newFixture() fixture {
	s := memory.NewStore()
	d := usecases.Deps{
		Assignments: s.Assignments(), ReadModels: s.ReadModels(), World: s.World(), Outbox: s.Outbox(), Inbox: s.Inbox(),
		UoW: s.UnitOfWork(), Clock: memory.NewClock(time.Date(2026, 10, 22, 14, 10, 0, 0, time.UTC)), IDs: &memory.SequentialIDs{},
	}
	return fixture{store: s, h: consumer.Handlers{UC: consumer.UseCases{
		Queue: usecases.NewQueueAssignment(d), Cancel: usecases.NewCancelAssignment(d), Revise: usecases.NewApplyRevisedPromise(d),
		Refusal: usecases.NewHandleNodeRefusal(d), Lock: usecases.NewLockAssignment(d), Ship: usecases.NewMarkShipped(d),
		Deliver: usecases.NewMarkDelivered(d), PickupMoved: usecases.NewRecordPickupMoved(d), Processed: usecases.NewRecordProcessed(d),
		Project: usecases.NewProjector(d),
	}}}
}

// example is one consumed message example of apis/asyncapi.yaml.
type example struct {
	Name, Topic string
	Event       map[string]any
}

// consumedExamples reads the CloudEvents examples of every `receive` message of the published contract.
func consumedExamples(t *testing.T) []example {
	t.Helper()
	raw, err := os.ReadFile("../../../../apis/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Channels map[string]struct {
			Address  string
			Messages map[string]any
		}
		Components struct {
			Messages map[string]struct {
				Examples []struct{ Payload map[string]any }
			}
		}
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var out []example
	for name, ch := range doc.Channels {
		if strings.HasPrefix(name, "orderSourcing") {
			continue
		}
		for msg := range ch.Messages {
			out = append(out, example{Name: msg, Topic: ch.Address, Event: doc.Components.Messages[msg].Examples[0].Payload})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (f fixture) route() map[string]map[string]consumer.HandlerFunc {
	m := map[string]map[string]consumer.HandlerFunc{}
	for _, th := range f.h.Topics() {
		m[th.Topic] = th.Handlers
	}
	return m
}

func (f fixture) consumerFor(topic string) *consumer.Consumer {
	return &consumer.Consumer{Name: topic, Topic: topic, Handlers: f.route()[topic], Logger: quiet()}
}

func bytesOf(t *testing.T, e map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Every consumed message the contract promises is routed on its topic, and its published example is applied
// without an error (an unknown assignment or a stale state is acknowledged, not retried).
func TestEveryConsumedExampleIsRoutedAndApplied(t *testing.T) {
	f := newFixture()
	examples := consumedExamples(t)
	if len(examples) != 32 {
		t.Fatalf("the contract has %d consumed examples, want 32", len(examples))
	}
	for _, ex := range examples {
		if _, ok := f.route()[ex.Topic][ex.Event["type"].(string)]; !ok {
			t.Errorf("%s: no handler for %v on %s", ex.Name, ex.Event["type"], ex.Topic)
			continue
		}
		if err := f.consumerFor(ex.Topic).HandleMessage(context.Background(), bytesOf(t, ex.Event)); err != nil {
			t.Errorf("%s: %v", ex.Name, err)
		}
	}
	// The handlers of a topic hold nothing the contract does not promise.
	promised := map[string]bool{}
	for _, ex := range examples {
		promised[ex.Event["type"].(string)] = true
	}
	for topic, hs := range f.route() {
		for typ := range hs {
			if !promised[typ] {
				t.Errorf("%s handles %s, which apis/asyncapi.yaml does not list", topic, typ)
			}
		}
	}
}

func event(t *testing.T, typ, id, subject string, data any) []byte {
	t.Helper()
	return bytesOf(t, map[string]any{
		"specversion": "1.0", "id": id, "source": "/retailnetwork/test", "type": typ, "subject": subject,
		"time": "2026-10-22T14:00:00Z", "datacontenttype": "application/json", "data": data,
	})
}

func TestOrderFlowThroughConsumedEvents(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	for _, ex := range consumedExamples(t) {
		if ex.Topic == consumer.CustomerOrderingTopic {
			continue
		}
		if err := f.consumerFor(ex.Topic).HandleMessage(ctx, bytesOf(t, ex.Event)); err != nil {
			t.Fatalf("%s: %v", ex.Name, err)
		}
	}
	for _, ex := range consumedExamples(t) {
		if ex.Name != "CustomerOrderPlaced" {
			continue
		}
		c := f.consumerFor(ex.Topic)
		if err := c.HandleMessage(ctx, bytesOf(t, ex.Event)); err != nil {
			t.Fatal(err)
		}
		list, err := f.store.Assignments().List(ctx, repository.AssignmentFilter{OrderID: "ord-51a0c9"})
		if err != nil || len(list) != 1 || list[0].GroupNo() != 1 {
			t.Fatalf("one assignment per placed group: %v %v", list, err)
		}
		before := f.store.Counts()
		if err := c.HandleMessage(ctx, bytesOf(t, ex.Event)); err != nil || f.store.Counts() != before {
			t.Fatalf("a redelivered event changes nothing: %v", err)
		}
		cancel := event(t, "com.retailnetwork.commerce.customer-ordering.customerorder.CustomerOrderCancelled", "evt-cancel", "ord-51a0c9",
			map[string]any{"orderId": "ord-51a0c9", "reason": "CUSTOMER_REQUEST", "cancelledAt": "2026-10-22T14:30:00Z", "groupNos": []int{1}})
		if err := c.HandleMessage(ctx, cancel); err != nil {
			t.Fatal(err)
		}
		got, _ := f.store.Assignments().FindByGroup(ctx, "ord-51a0c9", 1)
		if got.State() != assignment.StateCancelled {
			t.Fatalf("state = %s, want CANCELLED", got.State())
		}
	}
}

type countingDLQ struct{ got int }

func (d *countingDLQ) WriteMessages(_ context.Context, m ...kafkaMessage) error {
	d.got += len(m)
	return nil
}
func (d *countingDLQ) Close() error { return nil }

func TestPoisonAndNoiseHandling(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	typ := "com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderAccepted"
	c := f.consumerFor(consumer.VendorConnectivityTopic)

	if err := c.HandleMessage(ctx, []byte(`not json`)); err != nil {
		t.Fatalf("a message that is not a CloudEvent is skipped: %v", err)
	}
	if err := c.HandleMessage(ctx, event(t, "com.retailnetwork.partner.vendor-connectivity.purchaseorder.Other", "e0", "x", map[string]any{})); err != nil {
		t.Fatalf("an unrouted type is ignored: %v", err)
	}
	bad := event(t, typ, "e1", "po-1", map[string]any{"assignmentId": 7})
	if err := c.HandleMessage(ctx, bad); err != nil {
		t.Fatalf("without a DLQ a bad payload is skipped: %v", err)
	}
	c.DLQ = &countingDLQ{}
	if err := c.HandleMessage(ctx, bad); err == nil || !strings.Contains(err.Error(), "poison") {
		t.Fatalf("with a DLQ a bad payload is poison: %v", err)
	}
	invalid := event(t, "com.retailnetwork.commerce.customer-ordering.customerorder.CustomerOrderPlaced", "e2", "ord-x", map[string]any{
		"orderId": "ord-x", "destinationPostalPrefix": "30301",
		"groups": []any{map[string]any{"groupNo": 0, "promiseId": "prm-x", "tier": "NOPE", "deliverByLatest": "2026-10-25T00:00:00Z", "lines": []any{}}}})
	placing := f.consumerFor(consumer.CustomerOrderingTopic)
	placing.DLQ = &countingDLQ{}
	if err := placing.HandleMessage(ctx, invalid); err == nil || !strings.Contains(err.Error(), "poison") {
		t.Fatalf("a validation failure is poison: %v", err)
	}
	unknown := event(t, typ, "e3", "po-1", map[string]any{"assignmentId": "asg-ghost", "releaseNo": 1, "acceptedAt": "2026-10-22T14:00:00Z"})
	if err := c.HandleMessage(ctx, unknown); err != nil {
		t.Fatalf("an event about an assignment we do not own is acknowledged: %v", err)
	}
}

type failingRM struct {
	ports.ReadModels
}

func (failingRM) PutNode(context.Context, network_Node) error { return errTransient }

var errTransient = errors.New("database is down")

func TestTransientFailureIsReturnedForRetry(t *testing.T) {
	s := memory.NewStore()
	d := usecases.Deps{
		Assignments: s.Assignments(), ReadModels: failingRM{s.ReadModels()}, World: s.World(), Outbox: s.Outbox(), Inbox: s.Inbox(),
		UoW: s.UnitOfWork(), Clock: memory.NewClock(time.Now()), IDs: &memory.SequentialIDs{},
	}
	h := consumer.Handlers{UC: consumer.UseCases{Project: usecases.NewProjector(d)}}
	c := &consumer.Consumer{Name: "n", Topic: consumer.NodeTopologyTopic, Handlers: h.NodeTopology(), Logger: quiet()}
	msg := event(t, "com.retailnetwork.logistics.node-topology.node.NodeActivated", "e9", "n1", map[string]any{"nodeId": "n1", "activatedAt": "2026-10-22T10:00:00Z"})
	if err := c.HandleMessage(context.Background(), msg); !errors.Is(err, errTransient) {
		t.Fatalf("a transient failure must be returned so the offset is not committed: %v", err)
	}
}
