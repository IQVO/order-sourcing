package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/views"
	"github.com/claudioed/order-sourcing/internal/domain/policy"
)

// PolicyReader and PolicyPublisher are the use cases of /sourcing-policy.
type (
	// PolicyReader returns the current sourcing policy (version 0 = defaults).
	PolicyReader interface {
		Current(ctx context.Context) (views.SourcingPolicy, error)
	}
	// PolicyPublisher validates and stores the next policy version.
	PolicyPublisher interface {
		Publish(ctx context.Context, in policy.Input) (views.SourcingPolicy, error)
	}
)

// sourcingPolicyDTO is the wire shape of the SourcingPolicy (apis/openapi.yaml).
// publishedAt is absent on version 0, the built-in default.
type sourcingPolicyDTO struct {
	Version                   int        `json:"version"`
	CrossRegionSurchargeCents int        `json:"crossRegionSurchargeCents"`
	MaxCostDeviationPercent   int        `json:"maxCostDeviationPercent"`
	PublishedAt               *time.Time `json:"publishedAt,omitempty"`
}

// putPolicyRequest is the body of PUT /sourcing-policy: the whole document. An
// omitted lever takes its default, 0 (switched off).
type putPolicyRequest struct {
	CrossRegionSurchargeCents int `json:"crossRegionSurchargeCents"`
	MaxCostDeviationPercent   int `json:"maxCostDeviationPercent"`
}

func toPolicyDTO(v views.SourcingPolicy) sourcingPolicyDTO {
	return sourcingPolicyDTO{
		Version: v.Version, CrossRegionSurchargeCents: v.CrossRegionSurchargeCents, MaxCostDeviationPercent: v.MaxCostDeviationPercent,
		PublishedAt: optTime(v.PublishedAt),
	}
}

func (s *Server) handleGetPolicy(w http.ResponseWriter, r *http.Request) {
	v, err := s.PolicyGet.Current(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPolicyDTO(v))
}

func (s *Server) handlePutPolicy(w http.ResponseWriter, r *http.Request) {
	var body putPolicyRequest
	if err := decodeRequiredJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.PolicyPut.Publish(r.Context(), policy.Input{
		CrossRegionSurchargeCents: body.CrossRegionSurchargeCents, MaxCostDeviationPercent: body.MaxCostDeviationPercent,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPolicyDTO(v))
}

// decodeRequiredJSON is decodeOptionalJSON for a body that must be present: an
// empty body would silently reset every lever, so it is malformed.
func decodeRequiredJSON(r *http.Request, dest any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%w: a JSON body is required", errMalformedRequest)
		}
		return fmt.Errorf("%w: %w", errMalformedRequest, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: trailing data after the JSON body", errMalformedRequest)
	}
	return nil
}
