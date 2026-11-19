package features_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/cucumber/godog"

	"github.com/claudioed/order-sourcing/internal/adapters/inbound/reportshttp"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/analyticsstore"
	"github.com/claudioed/order-sourcing/internal/analytics/report"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

// This file drives the split-shipment block of GET /reports/sourcing-mix (ADR 0018). The
// scenarios release through the real use cases, fold the domain events the service published
// into the in-memory analytics twin the way cmd/os-projector folds the Kafka facts, and read the
// real reports router over it.

// splitWorld is the per-scenario analytics projection and the last report read.
type splitWorld struct {
	store *analyticsstore.MemoryStore
	seq   int
	days  []map[string]any
}

var split splitWorld

func registerSplitShipmentSteps(sc *godog.ScenarioContext, w *world) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		split = splitWorld{store: analyticsstore.NewMemoryStore()}
		return ctx, nil
	})
	sc.Step(`^order "([^"]*)" is placed with (\d+) (NEXT_DAY) groups of (\d+) units of "([^"]*)"$`,
		func(order string, groups int, tier string, units int, item string) error {
			return w.placeGroups(order, "", tier, groups, units, item)
		})
	sc.Step(`^shopper "([^"]*)" places order "([^"]*)" with (\d+) (CONSOLIDATED) groups of (\d+) units of "([^"]*)"$`,
		func(shopper, order string, groups int, tier string, units int, item string) error {
			return w.placeGroups(order, shopperKey(shopper), tier, groups, units, item)
		})
	sc.Step(`^I release every queued assignment$`, func() error {
		// One release at a time: a merged follower leaves the queue with its carrier.
		for range 50 {
			queued, err := w.store.Assignments().Queued(w.ctx(), "", 1)
			if err != nil || len(queued) == 0 {
				return err
			}
			w.run(func() error { _, err := usecases.NewReleaseToNode(w.deps).Execute(w.ctx(), queued[0]); return err })
			if w.lastErr != nil {
				return w.lastErr
			}
		}
		return fmt.Errorf("the queue did not drain in 50 releases")
	})
	sc.Step(`^the analytics projection folds what the service published$`, func() error { return split.fold(w) })
	sc.Step(`^I read the sourcing-mix report of (\d{4}-\d{2}-\d{2})$`, split.read)
	sc.Step(`^the report has no day$`, split.noDay)
	sc.Step(`^the day (\d{4}-\d{2}-\d{2}) has (\d+) split-shipment decisions of which (\d+) (?:is|are) split$`, split.counts)
	sc.Step(`^the day (\d{4}-\d{2}-\d{2}) has the split share ([0-9.]+)$`, split.share)
	sc.Step(`^the day (\d{4}-\d{2}-\d{2}) has no split share$`, split.noShare)
	sc.Step(`^the day (\d{4}-\d{2}-\d{2}) has the groups per decision (.+)$`, split.buckets)
}

func (sw *splitWorld) noDay() error {
	if len(sw.days) != 0 {
		return fmt.Errorf("the report has %d days, want none: %v", len(sw.days), sw.days)
	}
	return nil
}

func (sw *splitWorld) counts(day string, decisions, splits int) error {
	b, err := sw.block(day)
	if err != nil {
		return err
	}
	if b["decisions"] != float64(decisions) || b["splitDecisions"] != float64(splits) {
		return fmt.Errorf("decisions/splitDecisions = %v/%v, want %d/%d", b["decisions"], b["splitDecisions"], decisions, splits)
	}
	return nil
}

func (sw *splitWorld) share(day string, share float64) error {
	b, err := sw.block(day)
	if err != nil {
		return err
	}
	if b["splitShare"] != share {
		return fmt.Errorf("splitShare = %v, want %v", b["splitShare"], share)
	}
	return nil
}

func (sw *splitWorld) noShare(day string) error {
	b, err := sw.block(day)
	if err != nil {
		return err
	}
	if share, present := b["splitShare"]; !present || share != nil {
		return fmt.Errorf("splitShare = %v (present %v), want an explicit null", share, present)
	}
	return nil
}

func (sw *splitWorld) buckets(day, want string) error {
	b, err := sw.block(day)
	if err != nil {
		return err
	}
	buckets, _ := b["groupsPerDecision"].([]any)
	parts := make([]string, 0, len(buckets))
	for _, raw := range buckets {
		m, _ := raw.(map[string]any)
		parts = append(parts, fmt.Sprintf("%v:%v", m["bucket"], m["decisions"]))
	}
	if got := strings.Join(parts, ", "); got != want {
		return fmt.Errorf("groupsPerDecision = %q, want %q", got, want)
	}
	return nil
}

// placeGroups queues one order of n one-line groups of the tier; a key makes them one shopper's.
func (w *world) placeGroups(order, key, tier string, n, units int, item string) error {
	groups := make([]usecases.GroupCommand, 0, n)
	for no := 1; no <= n; no++ {
		groups = append(groups, usecases.GroupCommand{GroupNo: no, PromiseID: fmt.Sprintf("prm-%s-%d", order, no), Tier: assignment.Tier(tier),
			WitnessNodeID: "fc-a", DeliverByLatest: hours(48), Lines: []assignment.Line{{LineNo: 1, CatalogItemID: item, Quantity: units}}})
	}
	w.lastPlaced = usecases.QueueAssignmentCommand{Meta: usecases.Meta{EventID: "evt-" + order, EventType: orderPlacedType},
		OrderID: order, DestinationPostalPrefix: "30301", ConsolidationKey: key, Groups: groups}
	w.run(func() error { _, err := usecases.NewQueueAssignment(w.deps).Execute(w.ctx(), w.lastPlaced); return err })
	return w.lastErr
}

// fold applies every published decision and unsourceable declaration to the twin, exactly as
// cmd/os-projector maps them: a follower's fact names its carrier.
func (sw *splitWorld) fold(w *world) error {
	for _, e := range w.store.Outbox().Events() {
		var ev report.Event
		switch d := e.(type) {
		case assignment.FulfillmentSourcingDecided:
			ev = report.Event{Kind: report.KindFulfillmentDecided, AssignmentID: d.SubjectID(), OrderID: d.OrderID, GroupNo: d.GroupNo, OccurredAt: d.DecidedAt,
				Decision: report.Decision{ReleaseNo: d.ReleaseNo, NodeID: d.NodeID, Legs: d.Legs, EstimatedCostCents: d.EstimatedCostCents,
					CostComplete: d.CostComplete(), Units: d.Units, CandidateCount: d.CandidateCount, CarrierAssignmentID: string(d.CarrierAssignmentID)}}
		case assignment.FulfillmentUnsourceable:
			ev = report.Event{Kind: report.KindFulfillmentUnsourceable, AssignmentID: d.SubjectID(), OrderID: d.OrderID, OccurredAt: d.OccurredAt()}
		default:
			continue
		}
		sw.seq++
		ev.EventID, ev.At = fmt.Sprintf("evt-%d", sw.seq), e.OccurredAt()
		if _, err := sw.store.Apply(w.ctx(), ev); err != nil {
			return fmt.Errorf("fold %s: %w", ev.Kind, err)
		}
	}
	return nil
}

// read GETs the sourcing-mix report of one day through the real router.
func (sw *splitWorld) read(day string) error {
	h := reportshttp.NewRouter(&reportshttp.Server{Reader: sw.store}, slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/reports/sourcing-mix?from="+day+"&to="+day, nil))
	if rec.Code != http.StatusOK {
		return fmt.Errorf("sourcing-mix = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Days []map[string]any `json:"days"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		return err
	}
	sw.days = body.Days
	return nil
}

func (sw *splitWorld) block(day string) (map[string]any, error) {
	for _, d := range sw.days {
		if d["day"] == day {
			b, ok := d["splitShipment"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("day %s has no splitShipment object: %v", day, d)
			}
			return b, nil
		}
	}
	return nil, fmt.Errorf("the report has no day %s: %v", day, sw.days)
}
