// Package http is the REST inbound adapter (apis/openapi.yaml): list and get
// assignments, and the planner's reoptimise action. It maps wire shapes to
// use-case calls and typed errors to RFC 9457 problems; it holds no rules.
package http

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/application/views"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// Lister, Getter and Reoptimiser are the use cases the adapter calls.
type (
	// Lister lists assignments.
	Lister interface {
		ListAssignments(ctx context.Context, f repository.AssignmentFilter) ([]views.Assignment, error)
	}
	// Getter reads one assignment.
	Getter interface {
		GetAssignment(ctx context.Context, id assignment.ID) (views.Assignment, error)
	}
	// Reoptimiser re-runs the search of a queued assignment.
	Reoptimiser interface {
		Execute(ctx context.Context, id assignment.ID, expectedVersion int64) (views.Assignment, error)
	}
)

// Server wires the use cases to HTTP.
type Server struct {
	List        Lister
	Get         Getter
	Reoptimise  Reoptimiser
	Readiness   *Readiness
	Idempotency func(http.Handler) http.Handler // nil = no key required (unit tests only)
}

const reoptimiseSuffix = ":reoptimise"

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, statusBody{Status: "ok"}) })
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /assignments", s.handleList)
	// The Go mux cannot put a literal after a wildcard, so the custom-method
	// suffix is split off by hand.
	mux.HandleFunc("GET /assignments/{id}", s.handleGet)
	var post http.Handler = http.HandlerFunc(s.handleReoptimise)
	if s.Idempotency != nil {
		post = s.Idempotency(post)
	}
	mux.Handle("POST /assignments/{id}", post)
	return rejectNUL(mux)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := repository.AssignmentFilter{State: assignment.State(q.Get("state")), NodeID: q.Get("nodeId"), OrderID: q.Get("orderId")}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, r, shared.Invalid("limit %q is not an integer", raw))
			return
		}
		if n < 1 {
			writeError(w, r, shared.Invalid("limit must be between 1 and %d", repository.MaxLimit))
			return
		}
		f.Limit = n
	}
	list, err := s.List.ListAssignments(r.Context(), f)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := assignmentListDTO{Assignments: make([]assignmentDTO, 0, len(list))}
	for _, v := range list {
		out.Assignments = append(out.Assignments, toDTO(v))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	v, err := s.Get.GetAssignment(r.Context(), assignment.ID(r.PathValue("id")))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(v))
}

type reoptimiseRequest struct {
	AssignmentVersion int64 `json:"assignmentVersion"`
}

func (s *Server) handleReoptimise(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("id")
	id, ok := strings.CutSuffix(raw, reoptimiseSuffix)
	if !ok {
		writeProblem(w, r, notFoundResource, "no resource matches the requested path")
		return
	}
	var body reoptimiseRequest
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if body.AssignmentVersion < 0 {
		writeError(w, r, shared.Invalid("assignmentVersion must be at least 1"))
		return
	}
	v, err := s.Reoptimise.Execute(r.Context(), assignment.ID(id), body.AssignmentVersion)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(v))
}
