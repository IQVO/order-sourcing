package assignment

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// keyPattern is the consolidationKey customer-ordering derives: a keyed hash
// in 64 lower-case hexadecimal characters.
var keyPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validKey(k string) bool { return k == "" || keyPattern.MatchString(k) }

// ValidConsolidationKey reports whether k is empty (never merges) or a key in the
// form customer-ordering derives; a consumer drops a malformed one instead of
// failing the order (ADR 0015).
func ValidConsolidationKey(k string) bool { return validKey(k) }

// ConsolidationKey returns the optional customer-ordering key; "" never merges.
func (a *Assignment) ConsolidationKey() string { return a.consolidationKey }

// ConsolidatedInto returns the carrier this assignment follows, "" unless it
// is a follower of a merge.
func (a *Assignment) ConsolidatedInto() ID { return a.consolidatedInto }

// Members returns the follower ids of a carrier, ascending (empty otherwise).
func (a *Assignment) Members() []ID { return slices.Clone(a.members) }

// Merged reports whether the assignment is a member (carrier or follower) of a merge.
func (a *Assignment) Merged() bool { return a.consolidatedInto != "" || len(a.members) > 0 }

// MergedAssignmentIDs returns every merged assignment id of a carrier,
// carrier included, ascending; nil for any other assignment.
func (a *Assignment) MergedAssignmentIDs() []ID {
	if len(a.members) == 0 {
		return nil
	}
	return slices.Sorted(slices.Values(append(slices.Clone(a.members), a.id)))
}

// mergedIDs is the carrier first, then its followers ascending; nil unless a carrier.
func (a *Assignment) mergedIDs() []ID {
	if len(a.members) == 0 {
		return nil
	}
	return append([]ID{a.id}, a.members...)
}

// WindowBroken reports whether the candidate's delivery lies outside the
// promised window (after the latest, or before the earliest when known): after
// a promise revision it tells a merged release can no longer stand.
func (a *Assignment) WindowBroken() bool {
	if a.candidate == nil {
		return false
	}
	eta := a.candidate.ETA
	return eta.After(a.promise.DeliverByLatest) || (!a.promise.DeliverByEarliest.IsZero() && eta.Before(a.promise.DeliverByEarliest))
}

// checkMerge enforces the merge invariants of a snapshot (ADR 0015).
func checkMerge(s Snapshot) error {
	live := s.State == StateReleased || s.State.Committed()
	if s.ConsolidatedInto != "" {
		if err := checkFollowerLink(s, live); err != nil {
			return err
		}
	}
	if len(s.Members) == 0 {
		return nil
	}
	if s.ConsolidationKey == "" || !live {
		return errors.New("a carrier has a key and is RELEASED, LOCKED, SHIPPED or DELIVERED")
	}
	for i, m := range s.Members {
		if !shared.ValidID(string(m)) || m == s.ID || (i > 0 && s.Members[i-1] >= m) {
			return fmt.Errorf("members must be other assignment ids, ascending and distinct (position %d)", i)
		}
	}
	return nil
}

func checkFollowerLink(s Snapshot, live bool) error {
	switch {
	case !shared.ValidID(string(s.ConsolidatedInto)) || s.ConsolidatedInto == s.ID:
		return fmt.Errorf("consolidatedInto %q is not another assignment id", s.ConsolidatedInto)
	case len(s.Members) > 0:
		return errors.New("a follower has no members")
	case s.ConsolidationKey == "" || !live || s.Candidate == nil:
		return errors.New("a follower has a key, a candidate and is RELEASED, LOCKED, SHIPPED or DELIVERED")
	}
	return nil
}

// mergedRelease is what the carrier's release adds to the wire: the members,
// the union of their lines in member order, the member of each line and the
// minimum promised latest delivery.
type mergedRelease struct {
	members        []MergedMember
	lines          []Line
	lineMembers    []ID
	promisedLatest time.Time
}

func newMergedRelease(members []MergedMember) mergedRelease {
	m := mergedRelease{members: members}
	for i, member := range members {
		for _, l := range member.Lines {
			m.lines = append(m.lines, l)
			m.lineMembers = append(m.lineMembers, member.ID)
		}
		if i == 0 || member.DeliverByLatest.Before(m.promisedLatest) {
			m.promisedLatest = member.DeliverByLatest
		}
	}
	m.promisedLatest = shared.UTC(m.promisedLatest)
	return m
}

// checkFollowers validates the followers a carrier absorbs and returns them
// ascending by id: valid distinct ids other than the carrier's, and the union of
// lines within the bound.
func (a *Assignment) checkFollowers(followers []MergedMember) ([]MergedMember, error) {
	if a.consolidationKey == "" {
		return nil, shared.Invalid("an assignment with no consolidationKey does not merge")
	}
	if len(followers) < 1 {
		return nil, shared.Invalid("a merged release has at least one follower")
	}
	out := slices.Clone(followers)
	slices.SortFunc(out, func(x, y MergedMember) int { return strings.Compare(string(x.ID), string(y.ID)) })
	total := len(a.lines)
	for i, f := range out {
		if !shared.ValidID(string(f.ID)) || f.ID == a.id || (i > 0 && out[i-1].ID == f.ID) {
			return nil, shared.Invalid("follower %q is not a distinct id other than the carrier's", f.ID)
		}
		total += len(f.Lines)
	}
	if total > MaxLines {
		return nil, shared.Invalid("a merged release has at most %d lines, got %d", MaxLines, total)
	}
	return out, nil
}

// ReleaseConsolidated releases the carrier together with its followers at the
// merged candidate best (ReleaseToNode, ADR 0015). lines are the carrier's own
// lines resolved on the summed stock; followers carry theirs. It raises the one
// FulfillmentReleasedToNode (union lines, member list), the carrier's
// FulfillmentSourcingDecided and the analytics AssignmentsConsolidated.
func (a *Assignment) ReleaseConsolidated(best *Candidate, lines []Line, followers []MergedMember, facts SourcingFacts, now time.Time) ([]shared.Event, error) {
	if best == nil {
		return nil, shared.Invalid("a merged release needs a candidate")
	}
	sorted, err := a.checkFollowers(followers)
	if err != nil {
		return nil, err
	}
	events, rel, err := a.openRelease(best, lines, facts, now)
	if err != nil {
		return nil, err
	}
	a.members = make([]ID, 0, len(sorted))
	for _, f := range sorted {
		a.members = append(a.members, f.ID)
	}
	self := MergedMember{ID: a.id, OrderID: a.orderID, GroupNo: a.groupNo, PromiseID: a.promise.PromiseID,
		DeliverByLatest: a.promise.DeliverByLatest, Lines: cloneLines(a.lines)}
	merge := newMergedRelease(append([]MergedMember{self}, sorted...))
	decided := a.sourcingDecided(rel, *best, facts)
	decided.ConsolidatedCount = len(merge.members)
	return append(events, a.releasedToNode(rel, best, lines, &merge), decided, AssignmentsConsolidated{
		Base: a.base(), ReleaseNo: rel.No, NodeID: rel.NodeID, Members: merge.members, ConsolidatedAt: rel.ReleasedAt,
	}), nil
}

// ReleaseAsFollower releases a follower at the carrier's merged candidate with
// its lines resolved on the summed stock; count is the number of members. It
// raises only the analytics FulfillmentSourcingDecided: the carrier's one
// release event covers the purchase order.
func (a *Assignment) ReleaseAsFollower(carrier ID, best *Candidate, lines []Line, count int, facts SourcingFacts, now time.Time) ([]shared.Event, error) {
	if best == nil || count < 2 {
		return nil, shared.Invalid("a follower release needs a candidate and at least two members")
	}
	if !shared.ValidID(string(carrier)) || carrier == a.id || a.consolidationKey == "" {
		return nil, shared.Invalid("a follower needs a consolidationKey and a carrier other than itself")
	}
	events, rel, err := a.openRelease(best, lines, facts, now)
	if err != nil {
		return nil, err
	}
	a.consolidatedInto = carrier
	decided := a.sourcingDecided(rel, *best, facts)
	decided.CarrierAssignmentID, decided.ConsolidatedCount = carrier, count
	return append(events, decided), nil
}

// LockFollower locks a follower together with its carrier: its own pending
// release is accepted. merged is every merged id, ascending (the carrier's
// MergedAssignmentIDs). A repeat on a locked follower returns no events.
func (a *Assignment) LockFollower(merged []ID, acceptedAt, now time.Time) ([]shared.Event, error) {
	if a.consolidatedInto == "" {
		return nil, fmt.Errorf("%w: assignment %s is not a follower", shared.ErrIllegalTransition, a.id)
	}
	last, ok := a.lastRelease()
	if a.state.Committed() {
		if ok && last.Outcome == OutcomeAccepted {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: assignment %s is %s", ErrAssignmentLocked, a.id, a.state)
	}
	if !ok {
		return nil, fmt.Errorf("%w: assignment %s has no release", ErrReleaseNotPending, a.id)
	}
	rel, err := a.pendingRelease(last.No)
	if err != nil {
		return nil, err
	}
	return a.lock(rel, merged, acceptedAt, now), nil
}

// Dissolution says why a merge is withdrawn and who is in it.
type Dissolution struct {
	// Reason is PROMISE_REVISED (the only reason that withdraws a release).
	Reason string
	// TriggerID is the revised member.
	TriggerID ID
	// MemberIDs are the merged ids, carrier first.
	MemberIDs []ID
}

// Requeue returns a RELEASED member of a withdrawn merge to QUEUED (ADR 0015):
// its pending release becomes WITHDRAWN, the merge links are cleared and best
// is the member's own re-search with no extra node exclusion (nil means no
// candidate is left: UNSOURCEABLE). The carrier additionally raises
// FulfillmentReleaseWithdrawn (vendor connectivity cancels the purchase order)
// and AssignmentsDeconsolidated.
func (a *Assignment) Requeue(best *Candidate, releaseAt time.Time, d Dissolution, now time.Time) ([]shared.Event, error) {
	if a.state != StateReleased || !a.Merged() {
		return nil, fmt.Errorf("%w: assignment %s is not a RELEASED member of a merge", shared.ErrIllegalTransition, a.id)
	}
	rel, err := a.pendingRelease(len(a.releases))
	if err != nil {
		return nil, err
	}
	if best != nil {
		if err := a.checkCandidate(best); err != nil {
			return nil, err
		}
	}
	from, carrier := a.candidate.NodeID, len(a.members) > 0
	rel.Outcome = OutcomeWithdrawn
	a.consolidatedInto, a.members = "", nil
	a.touch(now)
	var events []shared.Event
	if carrier {
		events = append(events, FulfillmentReleaseWithdrawn{
			Base: a.base(), ReleaseNo: rel.No, OrderID: a.orderID, GroupNo: a.groupNo, Reason: d.Reason, WithdrawnAt: shared.UTC(now),
		})
	}
	if best == nil {
		a.markUnsourceable()
		events = append(events, a.unsourceable(ReasonNoCandidate))
	} else {
		a.state = StateQueued
		events = append(events, a.setCandidate(best, releaseAt, from, ReasonConsolidationWithdrawn)...)
	}
	if carrier {
		events = append(events, a.deconsolidated(rel.No, d.Reason, d.TriggerID, d.MemberIDs, now))
	}
	return events, nil
}

func (a *Assignment) deconsolidated(releaseNo int, reason string, trigger ID, members []ID, now time.Time) AssignmentsDeconsolidated {
	return AssignmentsDeconsolidated{
		Base: a.base(), ReleaseNo: releaseNo, Reason: reason, TriggerAssignmentID: trigger,
		MemberAssignmentIDs: slices.Clone(members), DeconsolidatedAt: shared.UTC(now),
	}
}

// endMerge clears the merge links of an assignment that left RELEASED through a
// purchase order refusal; a carrier (ids is its carrier-first member list)
// raises AssignmentsDeconsolidated with the refusal's reason.
func (a *Assignment) endMerge(events []shared.Event, releaseNo int, reason string, ids []ID, now time.Time) []shared.Event {
	a.consolidatedInto, a.members = "", nil
	if len(ids) == 0 {
		return events
	}
	return append(events, a.deconsolidated(releaseNo, reason, "", ids, now))
}
