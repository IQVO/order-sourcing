package architecture

// Fitness tests of the A2A inbound adapter (docs/adr/0009-a2a-inbound-adapter.md):
// the dependency rule, the read-only write gate, no outbound A2A client and a
// Kafka-free, outbox-free cmd/a2a. The source-scanning ones are built from
// small pure helpers over go/parser so each can be proven to fail on a
// fixture (TestA2ASensorsFailOnBadFixtures).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	archgo "github.com/arch-go/arch-go/api"
	"github.com/arch-go/arch-go/api/configuration"
)

const (
	a2aAdapterDir = "../adapters/inbound/a2a"
	a2aCmdDir     = "../../cmd/a2a"
	a2aClientPkg  = "github.com/a2aproject/a2a-go/v2/a2aclient"
)

// TestA2AAdapterDependencyRule: like the MCP adapter, the A2A adapter is
// additive. It may depend only on the application and domain layers (never on
// outbound adapters, never on cmd), and nothing else in this codebase may
// depend on it: cmd/a2a wires it up and nothing else touches it.
func TestA2AAdapterDependencyRule(t *testing.T) {
	moduleInfo := configuration.Load(modulePath)

	t.Run("a2a adapter depends only on application and domain", func(t *testing.T) {
		result := archgo.CheckArchitecture(moduleInfo, configuration.Config{
			DependenciesRules: []*configuration.DependenciesRule{
				{
					Package: "**.internal.adapters.inbound.a2a.**",
					ShouldOnlyDependsOn: &configuration.Dependencies{
						Internal: []string{
							"**.internal.adapters.inbound.a2a.**",
							"**.internal.application.**",
							"**.internal.domain.**",
						},
					},
				},
			},
		})
		assertArchGoPasses(t, result)
	})

	t.Run("nothing else depends on the a2a adapter", func(t *testing.T) {
		result := archgo.CheckArchitecture(moduleInfo, configuration.Config{
			DependenciesRules: []*configuration.DependenciesRule{
				{
					Package: "**.internal.**",
					ShouldNotDependsOn: &configuration.Dependencies{
						Internal: []string{"**.internal.adapters.inbound.a2a.**"},
					},
				},
			},
		})
		assertArchGoPasses(t, result)
	})
}

// a2aParseSource parses one Go source text; imports only, plus declarations.
func a2aParseSource(t *testing.T, name, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return f
}

func a2aParseFile(t *testing.T, path string) *ast.File {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return a2aParseSource(t, path, string(src))
}

// importPaths returns the import paths of one file.
func importPaths(f *ast.File) []string {
	var out []string
	for _, spec := range f.Imports {
		if p, err := strconv.Unquote(spec.Path.Value); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// importsMatching returns the imports of f that contain any needle.
func importsMatching(f *ast.File, needles ...string) []string {
	var out []string
	for _, p := range importPaths(f) {
		for _, n := range needles {
			if strings.Contains(p, n) {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// qualifiedNames returns every `pkg.Name` selector of f whose qualifier is
// pkg, as the sorted, de-duplicated list of Names.
func qualifiedNames(f *ast.File, pkg string) []string {
	seen := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == pkg {
				seen[sel.Sel.Name] = true
			}
		}
		return true
	})
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// namesOutside returns the names that are not in allowed.
func namesOutside(names []string, allowed ...string) []string {
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	var out []string
	for _, n := range names {
		if !ok[n] {
			out = append(out, n)
		}
	}
	return out
}

// depsFieldTypes returns "Name Type" for every field of the struct type named
// Deps declared in f.
func depsFieldTypes(f *ast.File) []string {
	var out []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name.Name == "Deps" {
				out = append(out, structFields(ts.Type)...)
			}
		}
	}
	return out
}

func structFields(expr ast.Expr) []string {
	st, ok := expr.(*ast.StructType)
	if !ok {
		return nil
	}
	var out []string
	for _, field := range st.Fields.List {
		for _, name := range field.Names {
			out = append(out, name.Name+" "+typeString(field.Type))
		}
	}
	return out
}

func typeString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return "*" + typeString(e.X)
	case *ast.SelectorExpr:
		return typeString(e.X) + "." + e.Sel.Name
	case *ast.Ident:
		return e.Name
	default:
		return "?"
	}
}

// readOnlyViolations reports how one non-test adapter file reaches beyond the
// read use case: any use case other than usecases.Queries, the transactional
// outbox package, or a Deps field that is not the Queries read use case.
func readOnlyViolations(f *ast.File) []string {
	var out []string
	for _, name := range namesOutside(qualifiedNames(f, "usecases"), "Queries") {
		out = append(out, "references usecases."+name+" (only the read use case usecases.Queries may be reachable)")
	}
	for _, p := range importsMatching(f, "internal/application/outbox", "internal/adapters/outbound") {
		out = append(out, "imports "+p)
	}
	for _, field := range depsFieldTypes(f) {
		if field != "Queries *usecases.Queries" {
			out = append(out, "Deps field "+field+" (Deps may carry only Queries *usecases.Queries)")
		}
	}
	return out
}

// goSourcesIn lists the non-test Go files directly in dir.
func goSourcesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			out = append(out, dir+"/"+e.Name())
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s has no Go sources; the sensor is looking in the wrong place", dir)
	}
	return out
}

// TestA2AAgentIsReadOnly is the write gate of docs/adr/0009: this context's
// MCP charter is read-only (docs/adr/0006), so the A2A agent may not offer a
// write either. No write use case is reachable from the adapter: Deps carries
// the Queries read use case and nothing else, and the transactional outbox is
// not imported.
func TestA2AAgentIsReadOnly(t *testing.T) {
	for _, path := range goSourcesIn(t, a2aAdapterDir) {
		for _, v := range readOnlyViolations(a2aParseFile(t, path)) {
			t.Errorf("%s: %s: the A2A agent is read-only (docs/adr/0009)", path, v)
		}
	}
	var seen bool
	for _, path := range goSourcesIn(t, a2aAdapterDir) {
		if len(depsFieldTypes(a2aParseFile(t, path))) > 0 {
			seen = true
		}
	}
	if !seen {
		t.Fatal("no Deps struct found in the A2A adapter; the sensor is looking in the wrong place")
	}
}

// outboundClientImports returns the imports of f that pull in the A2A client.
func outboundClientImports(f *ast.File) []string {
	return importsMatching(f, a2aClientPkg)
}

// TestA2AHasNoOutboundClient: this context calls no sibling, over A2A or
// anything else. The A2A client package may appear in test code (a wire test
// is a client) but in no production source under internal/ or cmd/.
func TestA2AHasNoOutboundClient(t *testing.T) {
	roots := []string{"..", "../../cmd"}
	var scanned int
	for _, root := range roots {
		for _, path := range goFilesUnder(t, root, false) {
			scanned++
			for _, p := range outboundClientImports(a2aParseFile(t, path)) {
				t.Errorf("%s imports %s: this context makes no outbound A2A call (docs/adr/0009)", path, p)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no Go sources; the sensor is looking in the wrong place")
	}
}

// binaryViolations reports what cmd/a2a (read-only, Postgres only) must not
// import: Kafka, the outbox (relay or publisher) and any use case but the
// read ones.
func binaryViolations(f *ast.File) []string {
	var out []string
	for _, p := range importsMatching(f, "segmentio/kafka-go", "internal/adapters/outbound/kafka", "internal/adapters/outbound/outbox", "internal/application/outbox", "internal/adapters/inbound/kafka") {
		out = append(out, "imports "+p+" (the A2A binary never dials Kafka, never publishes and never relays)")
	}
	for _, name := range namesOutside(qualifiedNames(f, "usecases"), "NewQueries") {
		out = append(out, "references usecases."+name+" (the A2A binary wires only the read use case)")
	}
	return out
}

// TestA2ABinaryIsKafkaFreeAndReadOnly pins what docs/adr/0009 says about
// cmd/a2a: it is cmd/mcp's composition (Postgres + the read use cases) and
// nothing more, so it can never insert an outbox row.
func TestA2ABinaryIsKafkaFreeAndReadOnly(t *testing.T) {
	for _, path := range goSourcesIn(t, a2aCmdDir) {
		for _, v := range binaryViolations(a2aParseFile(t, path)) {
			t.Errorf("%s: %s", path, v)
		}
	}
}

// TestA2ASensorsFailOnBadFixtures proves each sensor above can fail.
func TestA2ASensorsFailOnBadFixtures(t *testing.T) {
	writeUseCase := `package a2a
import "github.com/claudioed/order-sourcing/internal/application/usecases"
type Deps struct {
	Queries *usecases.Queries
	Reoptimise *usecases.Reoptimise
}
func f() { _ = usecases.NewQueries }
`
	if got := readOnlyViolations(a2aParseSource(t, "w.go", writeUseCase)); len(got) < 2 {
		t.Errorf("a write use case in Deps/body was not flagged: %v", got)
	}
	outboxImport := `package a2a
import _ "github.com/claudioed/order-sourcing/internal/application/outbox"
`
	if got := readOnlyViolations(a2aParseSource(t, "o.go", outboxImport)); len(got) != 1 {
		t.Errorf("an outbox import was not flagged: %v", got)
	}
	adapterImport := `package a2a
import _ "github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres"
`
	if got := readOnlyViolations(a2aParseSource(t, "p.go", adapterImport)); len(got) != 1 {
		t.Errorf("an outbound adapter import was not flagged: %v", got)
	}
	clean := `package a2a
import "github.com/claudioed/order-sourcing/internal/application/usecases"
type Deps struct{ Queries *usecases.Queries }
`
	if got := readOnlyViolations(a2aParseSource(t, "c.go", clean)); len(got) != 0 {
		t.Errorf("the compliant shape was flagged: %v", got)
	}

	client := `package x
import "github.com/a2aproject/a2a-go/v2/a2aclient"
var _ = a2aclient.NewFromCard
`
	if got := outboundClientImports(a2aParseSource(t, "client.go", client)); len(got) != 1 {
		t.Errorf("an a2aclient import was not flagged: %v", got)
	}

	kafkaBinary := `package main
import (
	_ "github.com/segmentio/kafka-go"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
)
var _ = usecases.NewCommands
`
	if got := binaryViolations(a2aParseSource(t, "main.go", kafkaBinary)); len(got) != 2 {
		t.Errorf("a Kafka import / write use case in the binary was not flagged: %v", got)
	}
	readBinary := `package main
import "github.com/claudioed/order-sourcing/internal/application/usecases"
var _ = usecases.NewQueries
`
	if got := binaryViolations(a2aParseSource(t, "main.go", readBinary)); len(got) != 0 {
		t.Errorf("the compliant binary was flagged: %v", got)
	}
}
