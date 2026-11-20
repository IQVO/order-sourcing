package http

import (
	"errors"
	"net/http"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// problemBaseURI is the namespace of this service's RFC 9457 `type` URIs.
const problemBaseURI = "https://errors.order-sourcing.retail-network.example/"

// errMalformedRequest marks a body or parameter the adapter itself rejects.
var errMalformedRequest = errors.New("request body is not valid JSON for this operation")

// problem is the fixed (status, slug, title) of one error category.
type problem struct {
	status int
	slug   string
	title  string
}

var methodNotAllowed = problem{http.StatusMethodNotAllowed, "method-not-allowed", "Method not allowed"}

var internalProblem = problem{http.StatusInternalServerError, "internal-error", "Internal server error"}

// Problems the middleware writes itself, before any use case runs.
var (
	idempotencyKeyRequired = problem{http.StatusBadRequest, "idempotency-key-required", "Idempotency-Key header is required"}
	idempotencyKeyInvalid  = problem{http.StatusBadRequest, "idempotency-key-invalid", "Idempotency-Key header is invalid"}
	idempotencyKeyReused   = problem{http.StatusConflict, "idempotency-key-reused", "Idempotency-Key was already used with a different request"}
)

// Problems of a paged collection GET.
var (
	invalidLimit          = problem{http.StatusBadRequest, "invalid-limit", "Invalid limit"}
	invalidCursor         = problem{http.StatusBadRequest, "invalid-cursor", "Invalid cursor"}
	unknownQueryParameter = problem{http.StatusBadRequest, "unknown-query-parameter", "Unknown query parameter"}
)

// catalogueEntry is one typed error, its problem and the replay class of that
// problem: what the Idempotency-Key middleware does with the answer (ADR 0019).
type catalogueEntry struct {
	err    error
	p      problem
	replay replayClass
}

// problemCatalogue maps every typed error to its problem, in match order. The
// specific domain errors come before the shared categories they wrap.
//
// Every entry declares its replay class (ADR 0019 holds the reason of each):
// FINAL when the answer is a function of the request alone, OUTCOME when the
// command committed state and then refused (none here: a refused request
// commits nothing), RETRYABLE when nothing was committed and the answer
// depends on state that can change (an optimistic-lock conflict, an assignment
// an event has not created yet, the state or version of the aggregate). When
// unsure the class is RETRYABLE: the refusal committed nothing, so running it
// again costs one more execution. A new entry without a class fails
// TestEveryCatalogueEntryDeclaresAReplayClass.
var problemCatalogue = []catalogueEntry{
	// The body is not what the operation takes: a function of the request alone, FINAL.
	{errMalformedRequest, problem{http.StatusBadRequest, "malformed-request", "Malformed request"}, replayFinal},
	// LOCKED, SHIPPED and DELIVERED never go back to QUEUED and an assignment is never deleted: the same
	// request can never pass, FINAL (ADR 0019, the lines of guardMovable and of the transitions into QUEUED).
	{assignment.ErrAssignmentLocked, problem{http.StatusConflict, "assignment-locked", "Assignment is locked"}, replayFinal},
	// A node's refusal or a promise revision moves the aggregate on; the state is not reached YET: RETRYABLE.
	{assignment.ErrReleaseInFlight, problem{http.StatusConflict, "release-in-flight", "A release is pending"}, replayRetryable},
	{assignment.ErrNodeExcluded, problem{http.StatusConflict, "node-excluded", "Node is excluded"}, replayRetryable},
	// The caller's version can be behind OR ahead of the stored one, and the stored one moves: RETRYABLE.
	{assignment.ErrStaleAssignmentVersion, problem{http.StatusConflict, "stale-assignment-version", "Stale assignment version"}, replayRetryable},
	// The candidate comes from event-fed read models and the promise can be revised: RETRYABLE.
	{assignment.ErrCandidateBreaksPromise, problem{http.StatusUnprocessableEntity, "candidate-breaks-promise", "Candidate breaks the promise"}, replayRetryable},
	// Assignments are created by FulfillmentQueued events, never by REST: the target may arrive later: RETRYABLE.
	{ports.ErrNotFound, problem{http.StatusNotFound, "not-found", "Resource not found"}, replayRetryable},
	{ports.ErrConcurrentUpdate, problem{http.StatusConflict, "concurrent-update", "The resource was modified concurrently"}, replayRetryable},
	{shared.ErrIllegalTransition, problem{http.StatusConflict, "illegal-transition", "Illegal transition"}, replayRetryable},
	// One slug for request-only causes (the policy ranges) and state-derived ones (a candidate built from read models): RETRYABLE.
	{shared.ErrValidation, problem{http.StatusUnprocessableEntity, "validation-failed", "Validation failed"}, replayRetryable},
}

// problemFor returns the problem of the first catalogue entry err matches,
// or the internal-error problem.
func problemFor(err error) problem {
	if entry, ok := entryFor(err); ok {
		return entry.p
	}
	return internalProblem
}

// entryFor returns the first catalogue entry err matches.
func entryFor(err error) (catalogueEntry, bool) {
	for _, entry := range problemCatalogue {
		if errors.Is(err, entry.err) {
			return entry, true
		}
	}
	return catalogueEntry{}, false
}

// replayOf returns the replay class of the first catalogue entry err matches;
// replayUnset for an error the catalogue does not know (an unmapped 5xx).
func replayOf(err error) replayClass {
	entry, _ := entryFor(err)
	return entry.replay
}
