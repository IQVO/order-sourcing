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

var internalProblem = problem{http.StatusInternalServerError, "internal-error", "Internal server error"}

// Problems the middleware writes itself, before any use case runs.
var (
	idempotencyKeyRequired = problem{http.StatusBadRequest, "idempotency-key-required", "Idempotency-Key header is required"}
	idempotencyKeyTooLong  = problem{http.StatusUnprocessableEntity, "validation-failed", "Validation failed"}
)

// problemCatalogue maps every typed error to its problem, in match order. The
// specific domain errors come before the shared categories they wrap.
var problemCatalogue = []struct {
	err error
	p   problem
}{
	{errMalformedRequest, problem{http.StatusBadRequest, "malformed-request", "Malformed request"}},
	{assignment.ErrAssignmentLocked, problem{http.StatusConflict, "assignment-locked", "Assignment is locked"}},
	{assignment.ErrReleaseInFlight, problem{http.StatusConflict, "release-in-flight", "A release is pending"}},
	{assignment.ErrNodeExcluded, problem{http.StatusConflict, "node-excluded", "Node is excluded"}},
	{assignment.ErrStaleAssignmentVersion, problem{http.StatusConflict, "stale-assignment-version", "Stale assignment version"}},
	{assignment.ErrCandidateBreaksPromise, problem{http.StatusUnprocessableEntity, "candidate-breaks-promise", "Candidate breaks the promise"}},
	{ports.ErrNotFound, problem{http.StatusNotFound, "not-found", "Resource not found"}},
	{ports.ErrConcurrentUpdate, problem{http.StatusConflict, "concurrent-update", "The resource was modified concurrently"}},
	{shared.ErrIllegalTransition, problem{http.StatusConflict, "illegal-transition", "Illegal transition"}},
	{shared.ErrValidation, problem{http.StatusUnprocessableEntity, "validation-failed", "Validation failed"}},
}

// problemFor returns the problem of the first catalogue entry err matches,
// or the internal-error problem.
func problemFor(err error) problem {
	for _, entry := range problemCatalogue {
		if errors.Is(err, entry.err) {
			return entry.p
		}
	}
	return internalProblem
}
