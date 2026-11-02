//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	kafkago "github.com/segmentio/kafka-go"
	"github.com/testcontainers/testcontainers-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The end-to-end proof that the shipped binary is really wired: the test boots the REAL entry point
// (run, in-process) against testcontainers Postgres and Kafka with nothing but the environment a
// deployment sets, and drives it only through REST and Kafka. It feeds the network and a placed order
// through the input topics, reads the effects from Postgres, REST and the produced topics, walks one
// assignment to DELIVERED, and finally takes Kafka away to watch /readyz follow it. Every wait is
// bounded; run it with -timeout 300s.

const (
	containerStartTimeout = 120 * time.Second
	topicEvents           = "retailnetwork.order-sourcing.events"
	topicAnalytics        = "retailnetwork.order-sourcing.analytics"
	ceTypePrefix          = "com.retailnetwork.commerce.order-sourcing.assignment."
)

var inputTopics = map[string]string{ // topic -> consumer-group variable
	"retailnetwork.customer-ordering.events":       "CUSTOMER_ORDERING_CONSUMER_GROUP",
	"retailnetwork.delivery-promise.events":        "DELIVERY_PROMISE_CONSUMER_GROUP",
	"retailnetwork.last-mile-delivery.events":      "LAST_MILE_DELIVERY_CONSUMER_GROUP",
	"retailnetwork.linehaul-transportation.events": "LINEHAUL_TRANSPORTATION_CONSUMER_GROUP",
	"retailnetwork.node-performance.events":        "NODE_PERFORMANCE_CONSUMER_GROUP",
	"retailnetwork.node-topology.events":           "NODE_TOPOLOGY_CONSUMER_GROUP",
	"retailnetwork.offer-availability.events":      "OFFER_AVAILABILITY_CONSUMER_GROUP",
	"retailnetwork.shipping-and-tracking.events":   "SHIPPING_AND_TRACKING_CONSUMER_GROUP",
	"retailnetwork.vendor-connectivity.events":     "VENDOR_CONNECTIVITY_CONSUMER_GROUP",
}

func startPostgres(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), containerStartTimeout)
	defer cancel()
	c, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("order_sourcing_e2e"), tcpostgres.WithUsername("e2e"), tcpostgres.WithPassword("e2e"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)))
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	url, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres dsn: %v", err)
	}
	return url
}

func startKafka(t *testing.T) (*tckafka.KafkaContainer, []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), containerStartTimeout)
	defer cancel()
	c, err := tckafka.Run(ctx, "confluentinc/confluent-local:7.6.1", tckafka.WithClusterID("order-sourcing-e2e"))
	if err != nil {
		t.Fatalf("start kafka: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	brokers, err := c.Brokers(ctx)
	if err != nil {
		t.Fatalf("kafka brokers: %v", err)
	}
	return c, brokers
}

func createTopic(t *testing.T, brokers []string, topic string) {
	t.Helper()
	conn, err := kafkago.Dial("tcp", brokers[0])
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	defer conn.Close()
	if err := conn.CreateTopics(kafkago.TopicConfig{Topic: topic, NumPartitions: 1, ReplicationFactor: 1}); err != nil {
		t.Fatalf("create topic %s: %v", topic, err)
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if parts, err := conn.ReadPartitions(topic); err == nil && len(parts) == 1 && parts[0].Leader.ID != 0 {
			return
		}
	}
	t.Fatalf("topic %s never got a leader", topic)
}

var seq int

// send publishes one CloudEvent as a sibling context would.
func send(t *testing.T, brokers []string, topic, typ, subject string, data any) string {
	t.Helper()
	seq++
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", seq)
	sendWithID(t, brokers, topic, id, typ, subject, data)
	return id
}

func sendWithID(t *testing.T, brokers []string, topic, id, typ, subject string, data any) {
	t.Helper()
	value, err := json.Marshal(map[string]any{
		"specversion": "1.0", "id": id, "source": "/retailnetwork/e2e", "type": typ, "subject": subject,
		"time": time.Now().UTC().Format(time.RFC3339), "datacontenttype": "application/json", "data": data,
	})
	if err != nil {
		t.Fatal(err)
	}
	w := &kafkago.Writer{Addr: kafkago.TCP(brokers...), Topic: topic, Balancer: &kafkago.Hash{}, RequiredAcks: kafkago.RequireAll, BatchTimeout: 10 * time.Millisecond}
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := w.WriteMessages(ctx, kafkago.Message{Key: []byte(subject), Value: value}); err != nil {
		t.Fatalf("produce to %s: %v", topic, err)
	}
}

type outEvent struct {
	Type, Subject, Source string
	Data                  map[string]any
	Raw                   map[string]any
}

// readUntil reads topic from the beginning until done(events) holds or the deadline passes.
func readUntil(t *testing.T, brokers []string, topic string, done func([]outEvent) bool) []outEvent {
	t.Helper()
	r := kafkago.NewReader(kafkago.ReaderConfig{Brokers: brokers, Topic: topic, Partition: 0, MinBytes: 1, MaxBytes: 1 << 20, MaxWait: 200 * time.Millisecond})
	defer r.Close()
	if err := r.SetOffset(kafkago.FirstOffset); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var events []outEvent
	for {
		if done(events) {
			return events
		}
		msg, err := r.ReadMessage(ctx)
		if err != nil {
			t.Fatalf("topic %s: condition not met after %d events: %v", topic, len(events), err)
		}
		var raw map[string]any
		if err := json.Unmarshal(msg.Value, &raw); err != nil {
			t.Fatalf("topic %s: not JSON: %v", topic, err)
		}
		data, _ := raw["data"].(map[string]any)
		events = append(events, outEvent{Type: raw["type"].(string), Subject: raw["subject"].(string), Source: raw["source"].(string), Data: data, Raw: raw})
	}
}

func count(events []outEvent, name string) int {
	n := 0
	for _, e := range events {
		if e.Type == ceTypePrefix+name {
			n++
		}
	}
	return n
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func eventually(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(150 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

type api struct {
	t    *testing.T
	base string
}

func (a api) do(method, path, key, body string) (int, http.Header, []byte) {
	a.t.Helper()
	req, err := http.NewRequest(method, a.base+path, strings.NewReader(body))
	if err != nil {
		a.t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

func (a api) assignments(query string) []map[string]any {
	a.t.Helper()
	code, _, body := a.do("GET", "/assignments?"+query, "", "")
	if code != 200 {
		return nil
	}
	var out struct{ Items []map[string]any }
	if err := json.Unmarshal(body, &out); err != nil {
		a.t.Fatalf("list body: %v", err)
	}
	return out.Items
}

func (a api) stateOf(order string) string {
	if l := a.assignments("orderId=" + order); len(l) == 1 {
		return l[0]["state"].(string)
	}
	return ""
}

func ts(d time.Duration) string {
	return time.Now().UTC().Add(d).Truncate(time.Second).Format(time.RFC3339)
}

const (
	pfxTopology = "com.retailnetwork.logistics.node-topology."
	pfxLinehaul = "com.retailnetwork.logistics.linehaul-transportation.departure."
	pfxWave     = "com.retailnetwork.logistics.last-mile-delivery.wave."
	pfxOffer    = "com.retailnetwork.commerce.offer-availability.listing."
	pfxOrder    = "com.retailnetwork.commerce.customer-ordering.customerorder."
	pfxPO       = "com.retailnetwork.partner.vendor-connectivity.purchaseorder."
)

func TestRealWiringEndToEnd(t *testing.T) {
	dbURL := startPostgres(t)
	kafkaC, brokers := startKafka(t)
	for topic := range inputTopics {
		createTopic(t, brokers, topic)
	}
	createTopic(t, brokers, topicEvents)
	createTopic(t, brokers, topicAnalytics)

	addr := freePort(t)
	t.Setenv("DATABASE_URL", dbURL)
	t.Setenv("KAFKA_BROKERS", strings.Join(brokers, ","))
	t.Setenv("HTTP_ADDR", addr)
	t.Setenv("OUTBOX_RELAY_INTERVAL", "100ms")
	t.Setenv("SWEEP_INTERVAL", "1s")
	t.Setenv("SHUTDOWN_DRAIN_DELAY", "0s")
	t.Setenv("LOG_LEVEL", "info")
	for topic, env := range inputTopics {
		t.Setenv(env, "order-sourcing-e2e-"+strings.TrimPrefix(topic, "retailnetwork."))
	}

	ctx, cancel := context.WithCancel(context.Background())
	exited := make(chan error, 1)
	go func() { exited <- run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-exited:
		case <-time.After(30 * time.Second):
			t.Error("run did not return after shutdown")
		}
	})

	a := api{t: t, base: "http://" + addr}
	eventually(t, "/readyz to answer 200", 90*time.Second, func() bool { c, _, _ := a.do("GET", "/readyz", "", ""); return c == 200 })

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	feedNetwork(t, brokers)
	feedListings(t, brokers)
	// The input topics are consumed independently, so the order is placed only once every local read model has
	// caught up (an order that finds no candidate is UNSOURCEABLE at once, by design).
	eventually(t, "the read models to catch up", 90*time.Second, func() bool {
		var active, deps, waves, listings, areas int
		q := func(sql string, dst *int) { _ = pool.QueryRow(context.Background(), sql).Scan(dst) }
		q(`SELECT count(*) FROM rm_nodes WHERE status = 'ACTIVE'`, &active)
		q(`SELECT count(*) FROM rm_departures WHERE scheduled`, &deps)
		q(`SELECT count(*) FROM rm_waves WHERE scheduled`, &waves)
		q(`SELECT count(*) FROM rm_listings WHERE available_qty = 10`, &listings)
		q(`SELECT count(*) FROM rm_service_areas`, &areas)
		t.Logf("read models: active=%d deps=%d waves=%d listings=%d areas=%d", active, deps, waves, listings, areas)
		return active == 4 && deps == 3 && waves == 2 && listings == 2 && areas == 1
	})

	// --- two orders: a standard one stays queued, a same-day one goes to the micro node at once ---
	send(t, brokers, "retailnetwork.customer-ordering.events", pfxOrder+"CustomerOrderPlaced", "ord-1", map[string]any{
		"orderId": "ord-1", "destinationPostalPrefix": "30301", "placedAt": ts(0),
		"groups": []any{map[string]any{"groupNo": 1, "promiseId": "prm-1", "tier": "TWO_DAY", "witnessNodeId": "n-fc-1",
			"shipBy": ts(5 * time.Hour), "deliverByEarliest": ts(20 * time.Hour), "deliverByLatest": ts(40 * time.Hour),
			"lines": []any{map[string]any{"lineNo": 1, "catalogItemId": "cat-1", "quantity": 2}}}},
	})
	send(t, brokers, "retailnetwork.customer-ordering.events", pfxOrder+"CustomerOrderPlaced", "ord-2", map[string]any{
		"orderId": "ord-2", "destinationPostalPrefix": "30301", "placedAt": ts(0),
		"groups": []any{map[string]any{"groupNo": 1, "promiseId": "prm-2", "tier": "SAME_DAY", "witnessNodeId": "n-mc-1",
			"shipBy": ts(2 * time.Hour), "deliverByEarliest": ts(8 * time.Hour), "deliverByLatest": ts(12 * time.Hour),
			"lines": []any{map[string]any{"lineNo": 1, "catalogItemId": "cat-2", "quantity": 1}}}},
	})
	eventually(t, "ord-1 queued and ord-2 released", 60*time.Second, func() bool {
		return a.stateOf("ord-1") == "QUEUED" && a.stateOf("ord-2") == "RELEASED"
	})
	t.Logf("assignments: %v", a.assignments(""))
	queued := a.assignments("orderId=ord-1")[0]
	cand := queued["candidate"].(map[string]any)
	if cand["nodeId"] != "n-fc-1" || len(cand["legs"].([]any)) != 1 || queued["releaseAt"] == nil {
		t.Fatalf("queued candidate = %v", queued)
	}
	released := a.assignments("orderId=ord-2")[0]
	rel := released["releases"].([]any)[0].(map[string]any)
	if rel["nodeId"] != "n-mc-1" || rel["outcome"] != "PENDING" || rel["releaseNo"].(float64) != 1 {
		t.Fatalf("release = %v", rel)
	}
	id1, id2 := queued["assignmentId"].(string), released["assignmentId"].(string)
	if !strings.HasPrefix(id1, "asg-") {
		t.Fatalf("assignment id %q", id1)
	}

	// --- Postgres holds what REST shows; the outbox drained; the inbox recorded the events ---
	var states, processed, unpublished int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM assignments WHERE state IN ('QUEUED','RELEASED')`).Scan(&states)
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM processed_events`).Scan(&processed)
	if states != 2 || processed < 15 {
		t.Fatalf("postgres: assignments=%d processed_events=%d", states, processed)
	}
	eventually(t, "the outbox to drain", 20*time.Second, func() bool {
		_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&unpublished)
		return unpublished == 0
	})

	// --- the exact CloudEvents on the output topics ---
	ev := readUntil(t, brokers, topicEvents, func(e []outEvent) bool { return count(e, "FulfillmentReleasedToNode") >= 1 })
	for _, e := range ev {
		if e.Type != ceTypePrefix+"FulfillmentReleasedToNode" || e.Subject != id2 || e.Raw["specversion"] != "1.0" || e.Raw["datacontenttype"] != "application/json" {
			continue
		}
		if e.Data["nodeId"] != "n-mc-1" || e.Data["releaseNo"].(float64) != 1 || e.Data["orderId"] != "ord-2" || e.Data["tier"] != "SAME_DAY" || e.Data["assignmentId"] != id2 {
			t.Fatalf("released data = %v", e.Data)
		}
		if !strings.HasSuffix(e.Raw["dataschema"].(string), ":v1") {
			t.Fatalf("dataschema = %v", e.Raw["dataschema"])
		}
	}
	an := readUntil(t, brokers, topicAnalytics, func(e []outEvent) bool { return count(e, "FulfillmentQueued") >= 2 })
	if count(an, "FulfillmentQueued") != 2 {
		t.Fatalf("analytics: %d queued events", count(an, "FulfillmentQueued"))
	}

	// --- REST: reoptimise (idempotent), refusals, problem details ---
	code, _, body := a.do("POST", "/assignments/"+id1+"/reoptimisations", "", `{}`)
	if code != 400 || !strings.Contains(string(body), "idempotency-key-required") {
		t.Fatalf("no key = %d %s", code, body)
	}
	code, _, first := a.do("POST", "/assignments/"+id1+"/reoptimisations", "re-1", `{}`)
	code2, hdr, again := a.do("POST", "/assignments/"+id1+"/reoptimisations", "re-1", `{}`)
	if code != 200 || code2 != 200 || string(first) != string(again) || hdr.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("reoptimise: %d %d replayed=%q\n%s\n%s", code, code2, hdr.Get("Idempotency-Replayed"), first, again)
	}
	if code, _, body := a.do("POST", "/assignments/"+id2+"/reoptimisations", "re-1", `{}`); code != 409 || !strings.Contains(string(body), "idempotency-key-reused") {
		t.Fatalf("a key reused on another request is 409 idempotency-key-reused: %d %s", code, body)
	}
	if code, hdr, body := a.do("POST", "/assignments/"+id2+"/reoptimisations", "re-2", `{}`); code != 409 || !strings.HasPrefix(hdr.Get("Content-Type"), "application/problem+json") {
		t.Fatalf("a released assignment cannot be re-optimised: %d %s", code, body)
	}
	if code, _, body := a.do("GET", "/assignments/asg-nope", "", ""); code != 404 || !strings.Contains(string(body), "not-found") {
		t.Fatalf("unknown id = %d %s", code, body)
	}
	if code, _, _ := a.do("GET", "/assignments?limit=0", "", ""); code != 400 {
		t.Fatalf("limit=0 = %d", code)
	}
	if code, _, _ := a.do("GET", "/assignments?state=BOGUS", "", ""); code != 422 {
		t.Fatalf("state=BOGUS = %d", code)
	}

	// --- the vendor accepts, ships, the carrier delivers: LOCKED -> SHIPPED -> DELIVERED ---
	po := "retailnetwork.vendor-connectivity.events"
	send(t, brokers, po, pfxPO+"PurchaseOrderAccepted", "po-1", map[string]any{"poNumber": "po-1", "assignmentId": id2, "releaseNo": 1, "nodeId": "n-mc-1", "acceptedAt": ts(0)})
	eventually(t, "ord-2 LOCKED", 30*time.Second, func() bool { return a.stateOf("ord-2") == "LOCKED" })
	send(t, brokers, po, pfxPO+"VendorShipmentConfirmed", "po-1", map[string]any{"poNumber": "po-1", "assignmentId": id2, "nodeId": "n-mc-1", "trackingNumbers": []string{"TRK1"}, "shippedAt": ts(0)})
	eventually(t, "ord-2 SHIPPED", 30*time.Second, func() bool { return a.stateOf("ord-2") == "SHIPPED" })
	deliveredID := send(t, brokers, "retailnetwork.shipping-and-tracking.events", "com.retailnetwork.logistics.shipping-and-tracking.journey.PackageDelivered", "TRK1",
		map[string]any{"trackingNumber": "TRK1", "assignmentId": id2, "orderId": "ord-2", "deliveredAt": ts(0)})
	eventually(t, "ord-2 DELIVERED", 30*time.Second, func() bool { return a.stateOf("ord-2") == "DELIVERED" })
	// A redelivery of the same CloudEvents id changes nothing (inbox).
	sendWithID(t, brokers, "retailnetwork.shipping-and-tracking.events", deliveredID, "com.retailnetwork.logistics.shipping-and-tracking.journey.PackageDelivered", "TRK1",
		map[string]any{"trackingNumber": "TRK1", "assignmentId": id2, "orderId": "ord-2", "deliveredAt": ts(time.Minute)})
	time.Sleep(2 * time.Second)

	// --- the customer cancels the queued group ---
	send(t, brokers, "retailnetwork.customer-ordering.events", pfxOrder+"CustomerOrderCancelled", "ord-1",
		map[string]any{"orderId": "ord-1", "reason": "CUSTOMER_REQUEST", "cancelledAt": ts(0), "groupNos": []int{1}})
	eventually(t, "ord-1 CANCELLED", 30*time.Second, func() bool { return a.stateOf("ord-1") == "CANCELLED" })

	ev = readUntil(t, brokers, topicEvents, func(e []outEvent) bool {
		return count(e, "FulfillmentLocked") >= 1 && count(e, "FulfillmentShipped") >= 1 && count(e, "FulfillmentCancelled") >= 1
	})
	for _, name := range []string{"FulfillmentLocked", "FulfillmentShipped", "FulfillmentCancelled"} {
		if count(ev, name) != 1 {
			t.Errorf("%s published %d times", name, count(ev, name))
		}
	}
	an = readUntil(t, brokers, topicAnalytics, func(e []outEvent) bool { return count(e, "FulfillmentDelivered") >= 1 })
	if count(an, "FulfillmentDelivered") != 1 {
		t.Fatalf("the redelivered PackageDelivered published %d FulfillmentDelivered", count(an, "FulfillmentDelivered"))
	}

	// --- Kafka goes away: /readyz follows, /healthz stays ---
	stop := 20 * time.Second
	if err := kafkaC.Stop(context.Background(), &stop); err != nil {
		t.Fatalf("stop kafka: %v", err)
	}
	eventually(t, "/readyz to report 503 dependency-unavailable", 60*time.Second, func() bool {
		c, _, b := a.do("GET", "/readyz", "", "")
		return c == 503 && strings.Contains(string(b), "dependency-unavailable") && strings.Contains(string(b), "kafka")
	})
	if c, _, _ := a.do("GET", "/healthz", "", ""); c != 200 {
		t.Fatalf("/healthz = %d while Kafka is down", c)
	}
}

func feedNetwork(t *testing.T, brokers []string) {
	t.Helper()
	topo := "retailnetwork.node-topology.events"
	for _, n := range []struct{ id, typ, sub, op string }{
		{"n-fc-1", "FULFILLMENT_CENTER", "SORTABLE", "VENDOR"},
		{"n-mc-1", "FULFILLMENT_CENTER", "MICRO", "VENDOR"},
		{"n-sc-1", "SORTATION_CENTER", "", "NETWORK"},
		{"n-ds-1", "DELIVERY_STATION", "", "NETWORK"},
	} {
		send(t, brokers, topo, pfxTopology+"node.NodeRegistered", n.id, map[string]any{"nodeId": n.id, "nodeType": n.typ, "subtype": n.sub, "operator": n.op, "regionId": "r1", "timezone": "UTC"})
		if n.op == "VENDOR" {
			send(t, brokers, topo, pfxTopology+"node.NodeOperationalProfileDeclared", n.id, map[string]any{"nodeId": n.id, "profileRevision": 1, "handlingTimeMinutes": 60})
		}
		send(t, brokers, topo, pfxTopology+"node.NodeActivated", n.id, map[string]any{"nodeId": n.id, "activatedAt": ts(0)})
	}
	send(t, brokers, topo, pfxTopology+"region.RegionDefined", "r1", map[string]any{"regionId": "r1", "code": "R1", "name": "Region One"})
	send(t, brokers, topo, pfxTopology+"servicearea.ServiceAreaAssigned", "30301", map[string]any{"postalPrefix": "30301", "deliveryStationNodeId": "n-ds-1", "regionId": "r1", "assignmentVersion": 1})
	send(t, brokers, topo, pfxTopology+"lane.LaneOpened", "lane-1", map[string]any{"laneId": "lane-1", "originNodeId": "n-fc-1", "destinationNodeId": "n-sc-1", "mode": "GROUND", "nominalTransitMinutes": 180})

	lh := "retailnetwork.linehaul-transportation.events"
	dep := func(id, kind, from, to string, cutoff, departs, arrives time.Duration) {
		send(t, brokers, lh, pfxLinehaul+"DepartureScheduled", id, map[string]any{"departureId": id, "laneId": "lane-1", "kind": kind, "originNodeId": from, "destinationNodeId": to,
			"cutoffAt": ts(cutoff), "departsAt": ts(departs), "arrivesAt": ts(arrives), "capacityPackages": 100})
	}
	dep("dep-pk-1", "PICKUP", "n-fc-1", "n-sc-1", 6*time.Hour, 6*time.Hour+30*time.Minute, 9*time.Hour)
	dep("dep-lh-1", "LINEHAUL", "n-sc-1", "n-ds-1", 10*time.Hour, 10*time.Hour+30*time.Minute, 14*time.Hour)
	dep("dep-pk-2", "PICKUP", "n-mc-1", "n-ds-1", 3*time.Hour, 3*time.Hour+30*time.Minute, 5*time.Hour)

	lm := "retailnetwork.last-mile-delivery.events"
	wave := func(id string, induct, from, to time.Duration) {
		send(t, brokers, lm, pfxWave+"DeliveryWaveScheduled", id, map[string]any{"waveId": id, "stationNodeId": "n-ds-1", "serviceDate": ts(0)[:10],
			"inductCutoffAt": ts(induct), "windowStart": ts(from), "windowEnd": ts(to), "stopCapacity": 500})
	}
	wave("wave-1", 15*time.Hour, 16*time.Hour, 22*time.Hour)
	wave("wave-2", 6*time.Hour, 7*time.Hour, 10*time.Hour)
}

func feedListings(t *testing.T, brokers []string) {
	t.Helper()
	oa := "retailnetwork.offer-availability.events"
	for _, l := range []struct{ id, item, node string }{{"lst-1", "cat-1", "n-fc-1"}, {"lst-2", "cat-2", "n-mc-1"}} {
		send(t, brokers, oa, pfxOffer+"ListingCreated", l.id, map[string]any{"listingId": l.id, "catalogItemId": l.item, "nodeId": l.node})
		send(t, brokers, oa, pfxOffer+"ListingAvailabilityChanged", l.id, map[string]any{"listingId": l.id, "catalogItemId": l.item, "nodeId": l.node, "availableQty": 10, "basis": "VENDOR_FEED", "asOf": ts(-time.Minute)})
	}
}
