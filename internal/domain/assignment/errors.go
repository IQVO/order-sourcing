package assignment

import "errors"

// The FulfillmentAssignment invariants of the model, by the model's names.
var (
	// ErrCandidateBreaksPromise: a released candidate's eta must not be after deliverByLatest (422).
	ErrCandidateBreaksPromise = errors.New("candidate breaks promise")
	// ErrAssignmentLocked: the node cannot change after LOCKED (409).
	ErrAssignmentLocked = errors.New("assignment locked")
	// ErrNodeExcluded: a node that refused is not released to again for the same promise revision (409).
	ErrNodeExcluded = errors.New("node excluded")
	// ErrReleaseInFlight: at most one release PENDING at a time (409).
	ErrReleaseInFlight = errors.New("release in flight")
	// ErrStaleAssignmentVersion: the caller's assignmentVersion is not the current one (409).
	ErrStaleAssignmentVersion = errors.New("stale assignment version")
	// ErrCancelAfterLock: cancel only before LOCKED (409).
	ErrCancelAfterLock = errors.New("cancel after lock")
	// ErrReleaseNotPending: the outcome is for a release that is not the pending one (a late
	// outcome of an old release). Consumers record it and move on (ADR 0002).
	ErrReleaseNotPending = errors.New("release not pending")
	// ErrStaleRevision: the promise revision is not above the applied one. Consumers ignore it.
	ErrStaleRevision = errors.New("stale promise revision")
)
