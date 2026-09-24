// Package archtest enforces the module boundaries of docs/architecture.md §5
// that depguard cannot express generically. It checks the real import graph
// reported by `go list`, including test imports.
package archtest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const (
	modulePath   = "github.com/Zefanrakh/kue-preorder"
	internalRoot = modulePath + "/internal/"
)

type goPackage struct {
	ImportPath   string
	Imports      []string
	TestImports  []string
	XTestImports []string
}

func (p goPackage) allImports() []string {
	return slices.Concat(p.Imports, p.TestImports, p.XTestImports)
}

// internalModule splits a path under internal/ into its module and the
// subpackage within it: ".../internal/catalog/postgres" is ("catalog", "postgres").
func internalModule(importPath string) (module, sub string, ok bool) {
	rest, ok := strings.CutPrefix(importPath, internalRoot)
	if !ok {
		return "", "", false
	}
	module, sub, _ = strings.Cut(rest, "/")
	return module, sub, true
}

func isPostgres(sub string) bool {
	return sub == "postgres" || strings.HasPrefix(sub, "postgres/")
}

// recipeViolations: recipe is a pure engine and imports nothing from internal/.
func recipeViolations(pkgs []goPackage) []string {
	var out []string
	for _, p := range pkgs {
		if m, _, ok := internalModule(p.ImportPath); !ok || m != "recipe" {
			continue
		}
		for _, imp := range p.allImports() {
			if m, _, ok := internalModule(imp); ok && m != "recipe" {
				out = append(out, fmt.Sprintf("%s imports %s: recipe must not import other internal packages", p.ImportPath, imp))
			}
		}
	}
	return out
}

// foreignPostgresViolations: a module never imports another module's postgres
// package. cmd/ is the composition root and may wire any adapter.
func foreignPostgresViolations(pkgs []goPackage) []string {
	var out []string
	for _, p := range pkgs {
		owner, _, ok := internalModule(p.ImportPath)
		if !ok {
			continue
		}
		for _, imp := range p.allImports() {
			if m, sub, ok := internalModule(imp); ok && m != owner && isPostgres(sub) {
				out = append(out, fmt.Sprintf("%s imports %s: go through the %s service interface instead", p.ImportPath, imp, m))
			}
		}
	}
	return out
}

// aggregationViolations: aggregation depends only on recipe, platform, and the
// root package (service interfaces) of other modules.
func aggregationViolations(pkgs []goPackage) []string {
	var out []string
	for _, p := range pkgs {
		if m, _, ok := internalModule(p.ImportPath); !ok || m != "aggregation" {
			continue
		}
		for _, imp := range p.allImports() {
			m, sub, ok := internalModule(imp)
			if !ok || sub == "" || m == "aggregation" || m == "recipe" || m == "platform" {
				continue
			}
			out = append(out, fmt.Sprintf("%s imports %s: aggregation may use only the interfaces in %s", p.ImportPath, imp, internalRoot+m))
		}
	}
	return out
}

func listPackages(t *testing.T) []goPackage {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", "list", "-json=ImportPath,Imports,TestImports,XTestImports", modulePath+"/...")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}

	var pkgs []goPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p goPackage
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		pkgs = append(pkgs, p)
	}
	if len(pkgs) == 0 {
		t.Fatal("go list returned no packages")
	}
	return pkgs
}

func TestArch_ModuleBoundaries(t *testing.T) {
	pkgs := listPackages(t)
	rules := []struct {
		name  string
		check func([]goPackage) []string
	}{
		{"recipe imports no internal package", recipeViolations},
		{"no module imports a foreign postgres package", foreignPostgresViolations},
		{"aggregation imports only recipe and module interfaces", aggregationViolations},
	}
	for _, rule := range rules {
		t.Run(rule.name, func(t *testing.T) {
			for _, v := range rule.check(pkgs) {
				t.Error(v)
			}
		})
	}
}

func TestArch_RulesDetectViolations(t *testing.T) {
	pkg := func(path string, imports ...string) goPackage {
		return goPackage{ImportPath: internalRoot + path, Imports: imports}
	}
	in := func(path string) string { return internalRoot + path }

	tests := []struct {
		name  string
		check func([]goPackage) []string
		pkg   goPackage
		want  bool
	}{
		{"recipe may import stdlib and expr", recipeViolations,
			pkg("recipe", "math", "github.com/expr-lang/expr"), false},
		{"recipe may import its own subpackage", recipeViolations,
			pkg("recipe", in("recipe/fit")), false},
		{"recipe must not import platform", recipeViolations,
			pkg("recipe", in("platform/clock")), true},
		{"recipe test must not import catalog", recipeViolations,
			goPackage{ImportPath: in("recipe"), XTestImports: []string{in("catalog")}}, true},

		{"module may import its own postgres", foreignPostgresViolations,
			pkg("catalog", in("catalog/postgres")), false},
		{"module may import another module's interfaces", foreignPostgresViolations,
			pkg("orders", in("catalog")), false},
		{"module must not import another module's postgres", foreignPostgresViolations,
			pkg("orders", in("catalog/postgres")), true},
		{"subpackage must not import another module's postgres", foreignPostgresViolations,
			pkg("orders/connect", in("payments/postgres/sqlc")), true},
		{"cmd may wire any postgres package", foreignPostgresViolations,
			goPackage{ImportPath: modulePath + "/cmd/api", Imports: []string{in("catalog/postgres")}}, false},

		{"aggregation may import recipe, platform, and interfaces", aggregationViolations,
			pkg("aggregation", in("recipe"), in("platform/db"), in("inventory"), in("aggregation/postgres")), false},
		{"aggregation must not import another module's connect", aggregationViolations,
			pkg("aggregation", in("catalog/connect")), true},
		{"aggregation must not import another module's postgres", aggregationViolations,
			pkg("aggregation", in("inventory/postgres")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.check([]goPackage{tt.pkg})
			if (len(got) > 0) != tt.want {
				t.Errorf("violations = %q, want violation: %v", got, tt.want)
			}
		})
	}
}
