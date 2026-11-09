//go:build integration

package features_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	kafkago "github.com/segmentio/kafka-go"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/claudioed/order-sourcing/internal/adapters/inbound/kafka"
	"github.com/claudioed/order-sourcing/internal/adapters/inbound/reportshttp"
	"github.com/claudioed/order-sourcing/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/analyticsstore"
	outboundkafka "github.com/claudioed/order-sourcing/internal/adapters/outbound/kafka"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/memory"
	relay "github.com/claudioed/order-sourcing/internal/adapters/outbound/outbox"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres"
	"github.com/claudioed/order-sourcing/internal/application/outbox"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
	"github.com/claudioed/order-sourcing/internal/testing/kafkatest"
	"github.com/claudioed/order-sourcing/internal/testing/pgtest"
)

// The whole analytics read side (ADR 0013) against real infrastructure, all
// started with testcontainers (never skip-gated, never a DSN from env): the REAL
// use cases queue, release, refuse and re-release assignments over the OLTP
// Postgres (read models included), the REAL encoder shapes the CloudEvents --
// FulfillmentSourcingDecided among them, in the same transaction as each
// release -- the relay drains them to a real Kafka (losing one ack, so a batch
// is republished under the SAME ids), the projector's consumer projects this
// context's own two topics into a SEPARATE analytical Postgres (its own
// container), and the reports answer from it over a read-only pool -- while
// legacy, unknown-type, poison and duplicate messages share the topics. The
// DLQ is read back, and a later marker event proves the duplicate was
// consumed, not stuck.

func TestMain(m *testing.M) {
	code := m.Run()
	kafkatest.Shutdown()
	pgtest.Shutdown()
	os.Exit(code)
}

func e2eAnalyticalPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	c, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("sourcing_analytics"), tcpostgres.WithUsername("itest"), tcpostgres.WithPassword("itest"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)))
	if err != nil {
		t.Fatalf("start the analytical postgres: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	url, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	return url
}

// lostAckOnce delivers to the real sink and THEN reports failure once: the
// "published, but the row was not marked" crash window, so the relay
// republishes the batch with the SAME CloudEvents ids.
type lostAckOnce struct {
	inner relay.Sink
	mu    sync.Mutex
	fired bool
}

func (f *lostAckOnce) Send(ctx context.Context, msgs ...outbox.Message) error {
	if err := f.inner.Send(ctx, msgs...); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.fired {
		f.fired = true
		return errors.New("simulated crash after the broker ack, before the row was marked published")
	}
	return nil
}

// e2eSeed stores the network of seedNetwork in the OLTP read models, plus the
// planner's cost of lane-1 and of the wave: fc-a's route (pk-a, lh-1, w1) is fully costed, fc-b's
// pickup runs on the undeclared lane-b, so its estimate is incomplete -- and,
// an undeclared lane contributing 0 (ADR 0011), cheaper: fc-b wins first.
func e2eSeed(t *testing.T, rm *postgres.ReadModels) {
	t.Helper()
	ctx := context.Background()
	pickupB := departure("pk-b", "fc-b", "sc-1", 4, 5, 8)
	pickupB.LaneID = "lane-b"
	for _, err := range []error{
		rm.PutNode(ctx, vendorNode("fc-a", "r1")),
		rm.PutNode(ctx, vendorNode("fc-b", "r2")),
		rm.PutNode(ctx, network.Node{ID: "sc-1", Type: "SORTATION_CENTER", Operator: "NETWORK", Status: network.StatusActive, SortDwellMinutes: 30}),
		rm.PutLane(ctx, network.Lane{ID: "lane-1", Origin: "fc-a", Destination: "ds-1", Mode: "GROUND", CostPerUnitCents: 125, CostDeclaredAt: hours(-1)}),
		rm.PutDeparture(ctx, departure("pk-a", "fc-a", "sc-1", 4, 5, 8)),
		rm.PutDeparture(ctx, pickupB),
		rm.PutDeparture(ctx, departure("lh-1", "sc-1", "ds-1", 10, 11, 14)),
		rm.PutWave(ctx, network.Wave{ID: "w1", StationID: "ds-1", InductCutoffAt: hours(15), WindowStart: hours(20), WindowEnd: hours(26),
			Capacity: 50, Remaining: 50, RemainingKnown: true, Scheduled: true, CostPerStopCents: 300, CostDeclaredAt: hours(-1)}),
		rm.PutServiceArea(ctx, network.ServiceArea{PostalPrefix: "30301", StationID: "ds-1", RegionID: "r1", Version: 1}),
		rm.PutListing(ctx, listing("fc-a", 10)),
		rm.PutListing(ctx, listing("fc-b", 10)),
	} {
		if err != nil {
			t.Fatalf("seed the network: %v", err)
		}
	}
}

// e2eWrites drives the real use cases over the OLTP database.
type e2eWrites struct {
	t    *testing.T
	deps usecases.Deps
}

func (e *e2eWrites) place(order string, groups ...usecases.GroupCommand) []assignment.ID {
	e.t.Helper()
	for i := range groups {
		groups[i].GroupNo, groups[i].PromiseID = i+1, fmt.Sprintf("prm-%s-%d", order, i+1)
		groups[i].Tier, groups[i].WitnessNodeID, groups[i].DeliverByLatest = "NEXT_DAY", "fc-a", hours(48)
	}
	out, err := usecases.NewQueueAssignment(e.deps).Execute(context.Background(), usecases.QueueAssignmentCommand{
		Meta: usecases.Meta{EventID: "evt-" + order, EventType: orderPlacedType}, OrderID: order, DestinationPostalPrefix: "30301", Groups: groups,
	})
	if err != nil {
		e.t.Fatalf("queue %s: %v", order, err)
	}
	ids := make([]assignment.ID, len(out))
	for i, v := range out {
		ids[i] = assignment.ID(v.AssignmentID)
	}
	return ids
}

func units(item string, qty int) usecases.GroupCommand {
	return usecases.GroupCommand{Lines: []assignment.Line{{LineNo: 1, CatalogItemID: item, Quantity: qty}}}
}

func (e *e2eWrites) release(id assignment.ID, wantNode string) {
	e.t.Helper()
	v, err := usecases.NewReleaseToNode(e.deps).Execute(context.Background(), id)
	if err != nil {
		e.t.Fatalf("release %s: %v", id, err)
	}
	if rels := v.Releases; len(rels) == 0 || rels[len(rels)-1].NodeID != wantNode {
		e.t.Fatalf("release %s = %+v, want node %s", id, v.Releases, wantNode)
	}
}

func (e *e2eWrites) reject(id assignment.ID, releaseNo int) {
	e.t.Helper()
	if _, err := usecases.NewHandleNodeRefusal(e.deps).Execute(context.Background(), usecases.HandleNodeRefusalCommand{
		Meta:         usecases.Meta{EventID: fmt.Sprintf("evt-reject-%s-%d", id, releaseNo), EventType: "e2e.PurchaseOrderRejected"},
		AssignmentID: id, ReleaseNo: releaseNo, Outcome: assignment.OutcomeRejected, ReasonCode: "E2E",
	}); err != nil {
		e.t.Fatalf("reject %s release %d: %v", id, releaseNo, err)
	}
}

func TestAnalyticsReadSide_EndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	brokers := kafkatest.Brokers(t)
	oltp := pgtest.NewPool(t)
	analyticalURL := e2eAnalyticalPostgres(t)
	if err := analyticsstore.RunMigrations(analyticalURL); err != nil {
		t.Fatal(err)
	}
	for _, topic := range append(kafka.OwnTopics(), kafka.AnalyticsDLQTopic) {
		kafkatest.CreateTopic(t, topic)
	}

	// ---- OLTP writes through the real use cases and the real encoder ----
	rm := postgres.NewReadModels(oltp)
	e2eSeed(t, rm)
	w := &e2eWrites{t: t, deps: usecases.Deps{
		Assignments: postgres.NewAssignmentRepository(oltp), ReadModels: rm, World: postgres.NewWorldReader(oltp),
		Outbox: postgres.NewOutbox(oltp, outboundkafka.Encoder{}), Inbox: postgres.NewInbox(oltp), UoW: postgres.NewUnitOfWork(oltp),
		Clock: memory.NewClock(t0), IDs: &memory.SequentialIDs{},
	}}
	// ord-1: a split order, both groups released to fc-b (r2: out of the destination's region, incomplete cost).
	split := w.place("ord-1", units("cat-1", 2), units("cat-1", 1))
	w.release(split[0], "fc-b")
	w.release(split[1], "fc-b")
	// ord-2: released to fc-b, rejected, re-released to fc-a (r1: in region, release 2, fully costed).
	moved := w.place("ord-2", units("cat-1", 3))[0]
	w.release(moved, "fc-b")
	w.reject(moved, 1)
	w.release(moved, "fc-a")
	// ord-3: nobody stocks cat-9, so the group is unsourceable at once.
	w.place("ord-3", units("cat-9", 1))

	// ---- the topics also carry messages the projector must survive ----
	legacy := []byte(`{"event_id":"e-1","event_type":"FulfillmentSourcingDecided","payload":{"assignmentId":"x"}}`)
	unknown := hand(t, "evt-unknown", cloudevents.Type("assignment", "FulfillmentTeleported"), "asg-u", hours(0), map[string]any{"assignmentId": "asg-u"})
	poison := hand(t, "evt-poison", cloudevents.Type("assignment", "FulfillmentSourcingDecided"), "asg-p", hours(0),
		map[string]any{"assignmentId": "asg-OTHER", "orderId": "o", "releaseNo": 1, "nodeId": "n", "legs": 0, "costComplete": false,
			"units": 1, "candidateCount": 1, "decidedAt": hours(0)}) // subject != aggregate id
	kafkatest.Produce(t, kafka.OwnAnalyticsTopic, kafkatest.Message{Key: "x", Value: legacy}, kafkatest.Message{Key: "asg-u", Value: unknown},
		kafkatest.Message{Key: "asg-p", Value: poison})

	// ---- relay -> real Kafka (with one lost ack: a republish under the SAME ids) ----
	sink := outboundkafka.NewRelaySink(brokers)
	t.Cleanup(func() { _ = sink.Close() })
	r := relay.NewOutboxRelay(postgres.NewOutbox(oltp, outboundkafka.Encoder{}), &lostAckOnce{inner: sink}, quiet, relay.WithInterval(50*time.Millisecond))
	relayDone := make(chan struct{})
	go func() { defer close(relayDone); _ = r.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-relayDone })

	// ---- the projector: consumer + writer pool over the analytical database ----
	writer, err := analyticsstore.NewPool(ctx, analyticalURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(writer.Close)
	consumer := kafka.NewAnalyticsConsumer(brokers, kafka.OwnTopics(), kafka.AnalyticsDLQTopic,
		fmt.Sprintf("os-projector-e2e-%d", time.Now().UnixNano()), analyticsstore.NewProjection(writer), quiet)
	consumer.Retry = kafka.RetryPolicy{Initial: 50 * time.Millisecond, Max: 200 * time.Millisecond}
	consumerDone := make(chan struct{})
	go func() { defer close(consumerDone); _ = consumer.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-consumerDone; _ = consumer.Close() })

	// ---- the reports over a READ-ONLY pool ----
	readOnly, err := analyticsstore.NewReadOnlyPool(ctx, analyticalURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(readOnly.Close)
	reader := analyticsstore.NewReader(readOnly)
	srv := httptest.NewServer(reportshttp.NewRouter(&reportshttp.Server{Reader: reader, Ping: reader, Now: func() time.Time { return hours(72) }}, quiet, "e2e"))
	t.Cleanup(srv.Close)
	const span = "?from=2026-10-22&to=2026-10-24"
	days := func(path string) []map[string]any {
		var out struct {
			Days []map[string]any `json:"days"`
		}
		getJSON(t, srv.URL+path+span, &out)
		return out.Days
	}
	waitFor(t, 90*time.Second, "four decisions, one unsourceable and three queued orders projected", func() bool {
		mix, splits := days("/reports/sourcing-mix"), days("/reports/order-splits")
		return len(mix) == 1 && mix[0]["groupsReleased"] == 4.0 && mix[0]["unsourceableCount"] == 1.0 &&
			len(splits) == 1 && splits[0]["ordersQueued"] == 3.0
	})
	pgtest.WaitAllPublished(t, oltp)

	wantMix := []map[string]any{{"day": "2026-10-22", "groupsReleased": 4.0, "inRegionShare": 0.25, "unknownRegionCount": 0.0,
		"meanLegs": 1.0, "reassignmentRate": 0.25, "unsourceableCount": 1.0}}
	if got := days("/reports/sourcing-mix"); !reflect.DeepEqual(got, wantMix) {
		t.Errorf("sourcing-mix = %v\nwant %v", got, wantMix)
	}
	wantSplits := []map[string]any{{"day": "2026-10-22", "ordersQueued": 3.0, "groupsPerOrder": 4.0 / 3, "splitOrderShare": 1.0 / 3}}
	if got := days("/reports/order-splits"); !reflect.DeepEqual(got, wantSplits) {
		t.Errorf("order-splits = %v\nwant %v", got, wantSplits)
	}
	assertCostToServe(t, days("/reports/cost-to-serve"), decisionsOf(t, oltp, split[0], split[1], moved))
	assertOnlyThePoisonWasDeadLettered(t, brokers, poison)

	// ---- a redelivered decision (same id) changes nothing; a later marker proves it was consumed ----
	var dup []byte
	if err := oltp.QueryRow(ctx, `SELECT value FROM outbox_events WHERE subject = $1 AND event_type LIKE '%FulfillmentSourcingDecided' LIMIT 1`,
		string(split[0])).Scan(&dup); err != nil {
		t.Fatal(err)
	}
	marker := hand(t, "evt-marker", cloudevents.Type("assignment", "FulfillmentUnsourceable"), "asg-marker", hours(24),
		map[string]any{"assignmentId": "asg-marker", "orderId": "ord-marker", "groupNo": 1, "promiseId": "p", "excludedNodes": []any{}, "reason": "NO_CANDIDATE"})
	kafkatest.Produce(t, kafka.OwnAnalyticsTopic, kafkatest.Message{Key: string(split[0]), Value: dup})
	kafkatest.Produce(t, kafka.OwnEventsTopic, kafkatest.Message{Key: "asg-marker", Value: marker})
	waitFor(t, 60*time.Second, "the marker projected", func() bool {
		mix := days("/reports/sourcing-mix")
		return len(mix) == 2 && mix[1]["unsourceableCount"] == 1.0
	})
	if day := days("/reports/sourcing-mix")[0]; day["groupsReleased"] != 4.0 {
		t.Errorf("after a redelivered decision groupsReleased = %v, want still 4 (idempotent on the CloudEvents id)", day["groupsReleased"])
	}
	var f struct {
		AsOf       *time.Time `json:"asOf"`
		LagSeconds *float64   `json:"lagSeconds"`
	}
	getJSON(t, srv.URL+"/reports/freshness", &f)
	if f.AsOf == nil || !f.AsOf.Equal(hours(24)) || f.LagSeconds == nil || *f.LagSeconds != 48*3600 {
		t.Errorf("freshness = %+v, want asOf = the marker's time and a 48h lag", f)
	}
}

// e2eDecision is the wire data of one FulfillmentSourcingDecided in the outbox.
type e2eDecision struct {
	AssignmentID       string `json:"assignmentId"`
	ReleaseNo          int    `json:"releaseNo"`
	NodeID             string `json:"nodeId"`
	InRegion           *bool  `json:"inRegion"`
	Legs               int    `json:"legs"`
	EstimatedCostCents *int64 `json:"estimatedCostCents"`
	CostComplete       bool   `json:"costComplete"`
	Units              int    `json:"units"`
	CandidateCount     int    `json:"candidateCount"`
}

// decisionsOf reads the LAST decision of each assignment from the OLTP outbox:
// what the encoder actually wrote in the release transactions.
func decisionsOf(t *testing.T, oltp *pgxpool.Pool, ids ...assignment.ID) []e2eDecision {
	t.Helper()
	out := make([]e2eDecision, 0, len(ids))
	for _, id := range ids {
		var raw []byte
		if err := oltp.QueryRow(context.Background(), `SELECT value FROM outbox_events WHERE subject = $1 AND event_type LIKE '%FulfillmentSourcingDecided'
			ORDER BY id DESC LIMIT 1`, string(id)).Scan(&raw); err != nil {
			t.Fatalf("the decision of %s: %v", id, err)
		}
		var ce struct {
			Data e2eDecision `json:"data"`
		}
		if err := json.Unmarshal(raw, &ce); err != nil {
			t.Fatal(err)
		}
		out = append(out, ce.Data)
	}
	return out
}

// assertCostToServe checks the decision facts the releases wrote and that the
// report is exactly their ADR 0013 aggregation over the LAST decision per
// assignment: two incomplete fc-b decisions and ord-2's complete fc-a
// re-release (its superseded fc-b release 1 does not count).
func assertCostToServe(t *testing.T, got []map[string]any, last []e2eDecision) {
	t.Helper()
	a, b, moved := last[0], last[1], last[2]
	for _, d := range []e2eDecision{a, b} {
		if d.NodeID != "fc-b" || d.ReleaseNo != 1 || d.InRegion == nil || *d.InRegion || d.Legs != 1 || d.CostComplete ||
			d.EstimatedCostCents != nil || d.CandidateCount != 2 {
			t.Fatalf("fc-b decision = %+v", d)
		}
	}
	if moved.NodeID != "fc-a" || moved.ReleaseNo != 2 || moved.InRegion == nil || !*moved.InRegion || !moved.CostComplete ||
		moved.EstimatedCostCents == nil || *moved.EstimatedCostCents <= 0 || moved.Units != 3 || moved.CandidateCount != 1 {
		t.Fatalf("the re-release decision = %+v", moved)
	}
	c := float64(*moved.EstimatedCostCents)
	want := []map[string]any{{"day": "2026-10-22", "costCompleteShare": 1.0 / 3, "costPerUnitCents": c / 3,
		"p50CostCentsPerGroup": c, "p90CostCentsPerGroup": c}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cost-to-serve = %v\nwant %v", got, want)
	}
}

// assertOnlyThePoisonWasDeadLettered reads the DLQ: exactly the poison bytes,
// nothing for the legacy or unknown-type messages.
func assertOnlyThePoisonWasDeadLettered(t *testing.T, brokers []string, poison []byte) {
	t.Helper()
	dlq := kafkago.NewReader(kafkago.ReaderConfig{Brokers: brokers, Topic: kafka.AnalyticsDLQTopic, Partition: 0, MinBytes: 1, MaxBytes: 10e6})
	defer func() { _ = dlq.Close() }()
	readCtx, stop := context.WithTimeout(context.Background(), 60*time.Second)
	defer stop()
	m, err := dlq.ReadMessage(readCtx)
	if err != nil {
		t.Fatalf("nothing reached the DLQ: %v", err)
	}
	if !bytes.Equal(m.Value, poison) {
		t.Errorf("DLQ value differs from the poison message:\n%s", m.Value)
	}
	quietCtx, quietStop := context.WithTimeout(context.Background(), 3*time.Second)
	defer quietStop()
	if extra, err := dlq.ReadMessage(quietCtx); err == nil {
		t.Errorf("a second message reached the DLQ (legacy and unknown types are skipped, not dead-lettered): %s", extra.Value)
	}
}

func hand(t *testing.T, id, typ, subject string, at time.Time, data map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"specversion": "1.0", "id": id, "source": "/retailnetwork/order-sourcing", "type": typ, "subject": subject,
		"datacontenttype": "application/json", "time": at.Format(time.RFC3339), "data": data,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func getJSON(t *testing.T, url string, into any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d %s", url, resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, into); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(timeout); ; time.Sleep(200 * time.Millisecond) {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
	}
}
