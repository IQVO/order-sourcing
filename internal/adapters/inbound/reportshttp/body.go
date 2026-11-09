package reportshttp

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/claudioed/order-sourcing/internal/analytics/report"
)

// Wire bodies (camelCase, apis/openapi.yaml tag `reports`). A share, mean or
// rate is null when its denominator is 0 (ADR 0013).

type mixDayBody struct {
	Day                string   `json:"day"`
	GroupsReleased     int      `json:"groupsReleased"`
	InRegionShare      *float64 `json:"inRegionShare"`
	UnknownRegionCount int      `json:"unknownRegionCount"`
	MeanLegs           *float64 `json:"meanLegs"`
	ReassignmentRate   *float64 `json:"reassignmentRate"`
	UnsourceableCount  int      `json:"unsourceableCount"`
}

type mixReportBody struct {
	From string       `json:"from"`
	To   string       `json:"to"`
	Days []mixDayBody `json:"days"`
}

func mixBody(rg report.Range, rows []report.MixRow) mixReportBody {
	out := mixReportBody{From: rg.FirstDay(), To: rg.LastDay(), Days: make([]mixDayBody, 0, len(rows))}
	for _, r := range rows {
		out.Days = append(out.Days, mixDayBody{
			Day: report.FormatDay(r.Day), GroupsReleased: r.Releases, InRegionShare: r.InRegionShare, UnknownRegionCount: r.UnknownRegion,
			MeanLegs: r.MeanLegs, ReassignmentRate: r.ReassignmentRate, UnsourceableCount: r.Unsourceable,
		})
	}
	return out
}

type splitDayBody struct {
	Day             string   `json:"day"`
	OrdersQueued    int      `json:"ordersQueued"`
	GroupsPerOrder  *float64 `json:"groupsPerOrder"`
	SplitOrderShare *float64 `json:"splitOrderShare"`
}

type splitsReportBody struct {
	From string         `json:"from"`
	To   string         `json:"to"`
	Days []splitDayBody `json:"days"`
}

func splitsBody(rg report.Range, rows []report.SplitRow) splitsReportBody {
	out := splitsReportBody{From: rg.FirstDay(), To: rg.LastDay(), Days: make([]splitDayBody, 0, len(rows))}
	for _, r := range rows {
		out.Days = append(out.Days, splitDayBody{
			Day: report.FormatDay(r.Day), OrdersQueued: r.Orders, GroupsPerOrder: r.GroupsPerOrder, SplitOrderShare: r.SplitOrderShare,
		})
	}
	return out
}

type costDayBody struct {
	Day                  string   `json:"day"`
	CostCompleteShare    *float64 `json:"costCompleteShare"`
	CostPerUnitCents     *float64 `json:"costPerUnitCents"`
	P50CostCentsPerGroup *float64 `json:"p50CostCentsPerGroup"`
	P90CostCentsPerGroup *float64 `json:"p90CostCentsPerGroup"`
}

type costReportBody struct {
	From string        `json:"from"`
	To   string        `json:"to"`
	Days []costDayBody `json:"days"`
}

func costBody(rg report.Range, rows []report.CostRow) costReportBody {
	out := costReportBody{From: rg.FirstDay(), To: rg.LastDay(), Days: make([]costDayBody, 0, len(rows))}
	for _, r := range rows {
		out.Days = append(out.Days, costDayBody{
			Day: report.FormatDay(r.Day), CostCompleteShare: r.CostCompleteShare, CostPerUnitCents: r.CostPerUnitCents,
			P50CostCentsPerGroup: r.P50, P90CostCentsPerGroup: r.P90,
		})
	}
	return out
}

type freshnessBody struct {
	AsOf       *time.Time `json:"asOf"`
	LagSeconds *float64   `json:"lagSeconds"`
}

// problemBaseURI is the namespace of this service's RFC 9457 `type` URIs (the
// same base as cmd/api's, apis/openapi.yaml info).
const problemBaseURI = "https://errors.order-sourcing.retail-network.example/"

// problem is one RFC 9457 problem type: its status, slug and title.
type problem struct {
	status int
	slug   string
	title  string
}

var (
	invalidRange          = problem{http.StatusBadRequest, "invalid-report-range", "The report range is invalid"}
	notFound              = problem{http.StatusNotFound, "resource-not-found", "Resource not found"}
	methodNotAllowed      = problem{http.StatusMethodNotAllowed, "method-not-allowed", "Method not allowed"}
	dependencyUnavailable = problem{http.StatusServiceUnavailable, "dependency-unavailable", "A runtime dependency is unavailable"}
	internalError         = problem{http.StatusInternalServerError, "internal-error", "Internal error"}
)

type problemBody struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail"`
	Instance string `json:"instance"`
}

func writeProblem(w http.ResponseWriter, r *http.Request, p problem, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.status)
	_ = json.NewEncoder(w).Encode(problemBody{Type: problemBaseURI + p.slug, Title: p.title, Status: p.status, Detail: detail, Instance: r.URL.EscapedPath()})
}

// logSafe strips line breaks so a logged value cannot forge a log line.
var logSafe = strings.NewReplacer("\n", " ", "\r", " ")

// writeInternal logs err and answers a 500 whose detail never leaks it.
func writeInternal(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "report query failed", "error", logSafe.Replace(err.Error()), "path", logSafe.Replace(r.URL.Path))
	writeProblem(w, r, internalError, "an unexpected error occurred")
}
