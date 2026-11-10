package policy

import (
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// SubjectPolicy is the subject and Kafka key of every policy event: the policy
// is one document, so its aggregate root id is the constant "policy".
const SubjectPolicy = "policy"

// SourcingPolicyPublished is raised by Publish. Analytics only (ADR 0014).
type SourcingPolicyPublished struct {
	shared.Header
	Number                    int
	CrossRegionSurchargeCents int
	MaxCostDeviationPercent   int
	PublishedAt               time.Time
}

// EventName returns "SourcingPolicyPublished".
func (SourcingPolicyPublished) EventName() string { return "SourcingPolicyPublished" }

// Entity returns "policy".
func (SourcingPolicyPublished) Entity() string { return "policy" }

// SubjectID returns "policy".
func (SourcingPolicyPublished) SubjectID() string { return SubjectPolicy }

// Stream returns the analytics stream.
func (SourcingPolicyPublished) Stream() string { return shared.StreamAnalytics }

var _ shared.Event = SourcingPolicyPublished{}
