package http

import (
	"net/http"

	"github.com/claudioed/order-sourcing/internal/adapters/inbound/http/page"
	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

// handleList is GET /assignments: keyset-paged by assignmentId ({items,nextCursor}).
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	if name := page.Unknown(r, "state", "nodeId", "orderId"); name != "" {
		writeProblem(w, r, unknownQueryParameter, "the query parameter "+sanitizeLog(name)+" is not declared by this operation")
		return
	}
	limit, err := page.Limit(r)
	if err != nil {
		writeProblem(w, r, invalidLimit, err.Error())
		return
	}
	after, err := page.After(r)
	if err != nil {
		writeProblem(w, r, invalidCursor, err.Error())
		return
	}
	q := r.URL.Query()
	f := repository.AssignmentFilter{
		State: assignment.State(q.Get("state")), NodeID: q.Get("nodeId"), OrderID: q.Get("orderId"),
		Limit: page.FetchSize(limit),
	}
	if after != nil {
		f.After = after.ID
	}
	list, err := s.List.ListAssignments(r.Context(), f)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]assignmentDTO, 0, len(list))
	for _, v := range list {
		items = append(items, toDTO(v))
	}
	writeJSON(w, http.StatusOK, page.Of(items, limit, func(d assignmentDTO) page.Position {
		return page.Position{Sort: d.AssignmentID, ID: d.AssignmentID}
	}))
}
