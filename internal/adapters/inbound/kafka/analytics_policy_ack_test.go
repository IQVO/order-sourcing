package kafka

import (
	"context"
	"log/slog"
	"testing"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/order-sourcing/internal/adapters/kafka/cloudevents"
)

// TestAnalyticsConsumerAcknowledgesSourcingPolicyPublishedSilently: the policy
// fact shares the analytics topic with the projected events (ADR 0014), no
// report needs it, so it is committed past with no projection, no dead letter
// and no warning.
func TestAnalyticsConsumerAcknowledgesSourcingPolicyPublishedSilently(t *testing.T) {
	warns := &warnCounter{}
	p, dlq := &fakeProjection{}, &recordedDLQ{}
	c := analyticsConsumer(p, dlq, slog.New(warns))
	value, err := cloudevents.New(cloudevents.Spec{ID: "evt-policy", Entity: entityPolicy, EventName: "SourcingPolicyPublished", Subject: "policy",
		Time: anEventTime, Stream: cloudevents.StreamAnalytics, Version: 1,
		Data: map[string]any{"version": 1, "crossRegionSurchargeCents": 250, "maxCostDeviationPercent": 20, "publishedAt": decidedRFC}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.HandleMessage(context.Background(), kafkago.Message{Value: value}); err != nil {
		t.Fatalf("HandleMessage = %v", err)
	}
	if warns.warns != 0 || len(p.events) != 0 || len(dlq.msgs) != 0 {
		t.Fatalf("warns=%d applied=%d dead-lettered=%d, want none", warns.warns, len(p.events), len(dlq.msgs))
	}
}
