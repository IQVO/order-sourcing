package a2a

import (
	"fmt"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/views"
)

// problemBaseURI namespaces this context's problem types. It is the SAME
// namespace and the SAME slugs the REST adapter uses
// (internal/adapters/inbound/http/errors.go), so a delegating agent sees one
// error vocabulary over REST, MCP and A2A.
const problemBaseURI = "https://errors.order-sourcing.retail-network.example/"

// problem is the RFC 7807/9457-shaped rejection an agent client receives
// instead of a result: domain rejections and request-shape failures are
// business answers, not transport errors, so they travel in the message
// envelope.
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Status int    `json:"status"`
}

// summary renders the one-line form used for the reply's text part.
func (p *problem) summary() string {
	return fmt.Sprintf("%s (%d): %s", p.Title, p.Status, p.Detail)
}

func newProblem(status int, slug, title, detail string) *problem {
	return &problem{Type: problemBaseURI + slug, Title: title, Detail: detail, Status: status}
}

// Request-shape problems. They are the A2A counterparts of the REST adapter's
// malformed-request (400); a missing required argument is a 400 as well.

func problemMalformedRequest(detail string) *problem {
	return newProblem(400, "malformed-request", "Malformed request", detail)
}

func problemUnknownSkill(skill string) *problem {
	return newProblem(400, "unknown-skill", "Unknown skill",
		fmt.Sprintf("skill %q is not offered by this agent; the Agent Card lists the supported skills", skill))
}

func problemInvalidArgument(detail string) *problem {
	return newProblem(400, "invalid-argument", "Invalid skill argument", detail)
}

// envelope is the structured payload of every skill answer: the skill id the
// answer belongs to and exactly one of Data (the skill result) or Problem
// (why there is no result).
type envelope struct {
	Skill   string     `json:"skill"`
	Data    *skillData `json:"data,omitempty"`
	Problem *problem   `json:"problem,omitempty"`
}

// skillData is the union of the skill result DTOs: each skill fills its own
// field, keeping the envelope flat and versioned as one contract. summary is
// the one-liner for the reply's text part and is not serialised.
type skillData struct {
	Assignments *assignmentListView `json:"assignmentList,omitempty"`
	Assignment  *assignmentView     `json:"assignment,omitempty"`
	summary     string
}

// --- DTOs (the REST/MCP bodies, camelCase like the REST contract) ---------------

type promiseView struct {
	PromiseID       string `json:"promiseId"`
	Tier            string `json:"tier"`
	DeliverByLatest string `json:"deliverByLatest"`
	WitnessNodeID   string `json:"witnessNodeId"`
	RevisionNo      int    `json:"revisionNo"`
}

type lineView struct {
	LineNo        int    `json:"lineNo"`
	CatalogItemID string `json:"catalogItemId"`
	ListingID     string `json:"listingId,omitempty"`
	Quantity      int    `json:"quantity"`
}

type candidateView struct {
	NodeID            string   `json:"nodeId"`
	PickupDepartureID string   `json:"pickupDepartureId"`
	PickupCutoffAt    string   `json:"pickupCutoffAt"`
	Legs              []string `json:"legs"`
	DeliveryWaveID    string   `json:"deliveryWaveId"`
	ETA               string   `json:"eta"`
	Cost              int64    `json:"cost"`
}

type releaseView struct {
	ReleaseNo      int    `json:"releaseNo"`
	NodeID         string `json:"nodeId"`
	ReleasedAt     string `json:"releasedAt"`
	RequiredShipBy string `json:"requiredShipBy"`
	Outcome        string `json:"outcome"`
	ReasonCode     string `json:"reasonCode,omitempty"`
}

type excludedNodeView struct {
	NodeID string `json:"nodeId"`
	Reason string `json:"reason"`
}

type assignmentView struct {
	AssignmentID            string             `json:"assignmentId"`
	OrderID                 string             `json:"orderId"`
	GroupNo                 int                `json:"groupNo"`
	DestinationPostalPrefix string             `json:"destinationPostalPrefix"`
	State                   string             `json:"state"`
	AssignmentVersion       int64              `json:"assignmentVersion"`
	Promise                 promiseView        `json:"promise"`
	Lines                   []lineView         `json:"lines"`
	Candidate               *candidateView     `json:"candidate,omitempty"`
	ReleaseAt               string             `json:"releaseAt,omitempty"`
	Releases                []releaseView      `json:"releases"`
	ExcludedNodes           []excludedNodeView `json:"excludedNodes"`
	LockedAt                string             `json:"lockedAt,omitempty"`
	LockReason              string             `json:"lockReason,omitempty"`
	PickupDepartureID       string             `json:"pickupDepartureId,omitempty"`
	TrackingNumbers         []string           `json:"trackingNumbers"`
	ShippedAt               string             `json:"shippedAt,omitempty"`
	DeliveredAt             string             `json:"deliveredAt,omitempty"`
	CancelReason            string             `json:"cancelReason,omitempty"`
}

type assignmentListView struct {
	Assignments []assignmentView `json:"assignments"`
}

// --- mapping ------------------------------------------------------------------

// instant formats an instant as RFC 3339 UTC; the zero time is "" (omitted).
func instant(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func toPromise(p views.Promise) promiseView {
	return promiseView{
		PromiseID: p.PromiseID, Tier: p.Tier, DeliverByLatest: instant(p.DeliverByLatest),
		WitnessNodeID: p.WitnessNodeID, RevisionNo: p.RevisionNo,
	}
}

func toLines(lines []views.Line) []lineView {
	out := make([]lineView, 0, len(lines))
	for _, l := range lines {
		out = append(out, lineView{LineNo: l.LineNo, CatalogItemID: l.CatalogItemID, ListingID: l.ListingID, Quantity: l.Quantity})
	}
	return out
}

func toCandidate(c *views.Candidate) *candidateView {
	if c == nil {
		return nil
	}
	return &candidateView{
		NodeID: c.NodeID, PickupDepartureID: c.PickupDepartureID, PickupCutoffAt: instant(c.PickupCutoffAt),
		Legs: append([]string{}, c.Legs...), DeliveryWaveID: c.DeliveryWaveID, ETA: instant(c.ETA), Cost: c.Cost,
	}
}

func toReleases(releases []views.Release) []releaseView {
	out := make([]releaseView, 0, len(releases))
	for _, r := range releases {
		out = append(out, releaseView{
			ReleaseNo: r.ReleaseNo, NodeID: r.NodeID, ReleasedAt: instant(r.ReleasedAt),
			RequiredShipBy: instant(r.RequiredShipBy), Outcome: r.Outcome, ReasonCode: r.ReasonCode,
		})
	}
	return out
}

func toExcluded(excluded []views.ExcludedNode) []excludedNodeView {
	out := make([]excludedNodeView, 0, len(excluded))
	for _, e := range excluded {
		out = append(out, excludedNodeView{NodeID: e.NodeID, Reason: e.Reason})
	}
	return out
}

func toAssignment(v views.Assignment) assignmentView {
	return assignmentView{
		AssignmentID: v.AssignmentID, OrderID: v.OrderID, GroupNo: v.GroupNo,
		DestinationPostalPrefix: v.DestinationPostalPrefix, State: v.State, AssignmentVersion: v.AssignmentVersion,
		Promise: toPromise(v.Promise), Lines: toLines(v.Lines), Candidate: toCandidate(v.Candidate),
		ReleaseAt: instant(v.ReleaseAt), Releases: toReleases(v.Releases), ExcludedNodes: toExcluded(v.ExcludedNodes),
		LockedAt: instant(v.LockedAt), LockReason: v.LockReason, PickupDepartureID: v.PickupDepartureID,
		TrackingNumbers: append([]string{}, v.TrackingNumbers...), ShippedAt: instant(v.ShippedAt),
		DeliveredAt: instant(v.DeliveredAt), CancelReason: v.CancelReason,
	}
}

// toAssignmentList maps the read-model list into the skill data, with its
// one-line summary.
func toAssignmentList(list []views.Assignment, f findAssignmentsArgs) *skillData {
	items := make([]assignmentView, 0, len(list))
	for _, a := range list {
		items = append(items, toAssignment(a))
	}
	return &skillData{
		Assignments: &assignmentListView{Assignments: items},
		summary:     fmt.Sprintf("%d assignment(s)%s", len(items), f.describe()),
	}
}

// toAssignmentDetail maps one read-model assignment into the skill data, with
// its one-line summary.
func toAssignmentDetail(v views.Assignment) *skillData {
	dto := toAssignment(v)
	where := "no node yet"
	switch {
	case dto.Candidate != nil:
		where = "candidate node " + dto.Candidate.NodeID
	case len(dto.Releases) > 0:
		where = "latest release on node " + dto.Releases[len(dto.Releases)-1].NodeID
	}
	return &skillData{
		Assignment: &dto,
		summary: fmt.Sprintf("%s: %s, order %s group %d, %s (version %d)",
			dto.AssignmentID, dto.State, dto.OrderID, dto.GroupNo, where, dto.AssignmentVersion),
	}
}
