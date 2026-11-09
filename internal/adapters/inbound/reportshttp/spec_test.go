package reportshttp

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// specText is apis/openapi.yaml, the contract of the reports surface (tag
// reports, ADR 0013).
func specText(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../../../apis/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Every /reports path in the spec is a GET this router serves, and the router
// serves no /reports route the spec does not document.
func TestSpecReportsPathsMatchTheRouter(t *testing.T) {
	spec := specText(t)
	var documented []string
	for _, m := range regexp.MustCompile(`(?m)^  (/reports/[a-z-]+):$`).FindAllStringSubmatch(spec, -1) {
		documented = append(documented, m[1])
	}
	var served []string
	router := NewRouter(&Server{}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	err := chi.Walk(router.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/reports/") {
			if method != http.MethodGet {
				t.Errorf("%s %s: the reports surface is GET-only", method, route)
			}
			served = append(served, route)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(documented)
	slices.Sort(served)
	if len(documented) != 4 || !slices.Equal(documented, served) {
		t.Fatalf("spec /reports paths %v != router %v", documented, served)
	}
}

// Every JSON field the wire bodies write is a property name in the spec.
func TestSpecDocumentsEveryWireField(t *testing.T) {
	spec := specText(t)
	for _, v := range []any{mixDayBody{}, mixReportBody{}, splitDayBody{}, splitsReportBody{}, costDayBody{}, costReportBody{}, freshnessBody{}} {
		typ := reflect.TypeOf(v)
		for i := range typ.NumField() {
			name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
			if !strings.Contains(spec, "\n        "+name+":\n") {
				t.Errorf("%s.%s (json %q) is not a property in apis/openapi.yaml", typ.Name(), typ.Field(i).Name, name)
			}
		}
	}
	if !strings.Contains(spec, "invalid-report-range") {
		t.Error("the invalid-report-range problem slug is not documented")
	}
}
