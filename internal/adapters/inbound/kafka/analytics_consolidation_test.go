package kafka

import (
	"context"
	"log/slog"
	"testing"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/order-sourcing/internal/adapters/kafka/cloudevents"
)

// TestAnalyticsProjectionCountsAMergedPackageOnce: a follower's decision names
// its carrier and books 0 cents (the carrier's fact holds the cost), keeping
// its own units; the carrier's decision keeps its cents (ADR 0015).
func TestAnalyticsProjectionCountsAMergedPackageOnce(t *testing.T) {
	carrier := decisionData()
	carrier["consolidatedCount"] = 2
	follower := decisionData()
	follower["assignmentId"], follower["carrierAssignmentId"], follower["consolidatedCount"] = "asg-2", "asg-1", 2
	follower["units"] = 4
	p, _ := handleOne(t, ownEvent(t, "FulfillmentSourcingDecided", cloudevents.StreamAnalytics, "asg-1", carrier))
	f, _ := handleOne(t, ownEvent(t, "FulfillmentSourcingDecided", cloudevents.StreamAnalytics, "asg-2", follower))
	if len(p.events) != 1 || p.events[0].Decision.EstimatedCostCents == nil || *p.events[0].Decision.EstimatedCostCents != 450 {
		t.Fatalf("the carrier keeps its cost: %+v", p.events)
	}
	if len(f.events) != 1 {
		t.Fatalf("the follower must be projected: %+v", f.events)
	}
	d := f.events[0].Decision
	if d.EstimatedCostCents == nil || *d.EstimatedCostCents != 0 || !d.CostComplete || d.Units != 4 {
		t.Fatalf("a follower books 0 cents and its own units: %+v", d)
	}
}

// TestAnalyticsConsumerAcknowledgesTheConsolidationFactsSilently: the three new
// types share the topics with the projected events, no report needs them yet,
// so they are committed past with no projection, no dead letter and no warning.
func TestAnalyticsConsumerAcknowledgesTheConsolidationFactsSilently(t *testing.T) {
	for name, stream := range map[string]string{
		"AssignmentsConsolidated": cloudevents.StreamAnalytics, "AssignmentsDeconsolidated": cloudevents.StreamAnalytics,
		"FulfillmentReleaseWithdrawn": cloudevents.StreamEvents,
	} {
		t.Run(name, func(t *testing.T) {
			warns := &warnCounter{}
			p, dlq := &fakeProjection{}, &recordedDLQ{}
			c := analyticsConsumer(p, dlq, slog.New(warns))
			value := ownEvent(t, name, stream, "asg-1", map[string]any{"carrierAssignmentId": "asg-1", "assignmentId": "asg-1"})
			if err := c.HandleMessage(context.Background(), kafkago.Message{Value: value}); err != nil {
				t.Fatalf("HandleMessage = %v", err)
			}
			if warns.warns != 0 || len(p.events) != 0 || len(dlq.msgs) != 0 {
				t.Fatalf("warns=%d applied=%d dead-lettered=%d, want none", warns.warns, len(p.events), len(dlq.msgs))
			}
		})
	}
}
