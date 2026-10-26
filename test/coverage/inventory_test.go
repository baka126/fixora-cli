// Package coverage guards the reviewed remediation inventory. It does not
// enable a repair or require access to a cluster or AI provider.
package coverage

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

type inventory struct {
	Analyzers       []string `json:"analyzers"`
	PrecisionChecks []string `json:"precisionChecks"`
	Findings        []entry  `json:"findings"`
}
type entry struct {
	Source  string `json:"source"`
	Status  string `json:"status"`
	Profile string `json:"profile"`
}

// New detectors must receive a reviewed outcome instead of silently expanding
// the advertised repair surface. Dynamic API reasons are documented separately.
func TestRemediationInventoryCoversDetectors(t *testing.T) {
	inv := readInventory(t)
	definitions := analyzer.ListAnalyzers(nil)
	names := make([]string, 0, len(definitions))
	for _, def := range definitions {
		names = append(names, def.Name)
	}
	checkNames(t, "analyzer", names, inv.Analyzers)
	root := filepath.Join("..", "..", "internal", "analyzer")
	files, err := filepath.Glob(filepath.Join(root, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	actual := map[string]bool{}
	var precision []string
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		add := func(expr ast.Expr) {
			if value := statusLiteral(expr); value != "" {
				actual[filepath.Base(path)+":"+value] = true
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.KeyValueExpr:
				if key, ok := n.Key.(*ast.Ident); ok {
					if key.Name == "Status" || key.Name == "status" {
						add(n.Value)
					}
					if filepath.Base(path) == "precision.go" && key.Name == "name" {
						if value := statusLiteral(n.Value); value != "" {
							precision = append(precision, value)
						}
					}
				}
			case *ast.AssignStmt:
				for i, lhs := range n.Lhs {
					if i >= len(n.Rhs) {
						continue
					}
					if id, ok := lhs.(*ast.Ident); ok && id.Name == "status" {
						add(n.Rhs[i])
					}
					if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Status" {
						add(n.Rhs[i])
					}
				}
			case *ast.CallExpr:
				if id, ok := n.Fun.(*ast.Ident); ok {
					index, known := map[string]int{"networkPolicyFinding": 1, "configMapFinding": 1, "podSecurityFinding": 1, "pvcFinding": 1, "secretFinding": 2, "secretFindingID": 2, "olmFinding": 2, "clusterFinding": 2}[id.Name]
					if known && len(n.Args) > index {
						add(n.Args[index])
					}
				}
			case *ast.FuncDecl:
				if n.Name.Name == "podProblem" || n.Name.Name == "classifyCertExpiry" {
					ast.Inspect(n.Body, func(child ast.Node) bool {
						if ret, ok := child.(*ast.ReturnStmt); ok && len(ret.Results) > 0 {
							add(ret.Results[0])
						}
						return true
					})
				}
			}
			return true
		})
	}
	checkNames(t, "precision check", precision, inv.PrecisionChecks)
	documented := map[string]bool{}
	profiles := map[string]bool{"diagnosis": true, "review": true, "image": true, "resources": true, "startup": true, "config": true, "probe": true, "filesystem": true, "connection": true, "service-routing": true, "pvc-policy": true, "healthy": true, "security": true}
	for _, e := range inv.Findings {
		key := e.Source + ":" + e.Status
		if documented[key] {
			t.Errorf("duplicate inventory entry %s", key)
		}
		documented[key] = true
		if !profiles[e.Profile] {
			t.Errorf("unknown outcome profile %s for %s", e.Profile, key)
		}
	}
	for key := range actual {
		if !documented[key] {
			t.Errorf("unreviewed finding %s; add its outcome to remediation.json and the coverage reference", key)
		}
	}
	for _, status := range []string{"MissingStorageClass", "Pending", "SmallStorageRequest", "VolumeResizePending"} {
		if !documented["pvc.go:"+status] {
			t.Errorf("PVC repair outcome %s is not inventoried", status)
			continue
		}
		for _, e := range inv.Findings {
			if e.Source == "pvc.go" && e.Status == status && e.Profile != "pvc-policy" {
				t.Errorf("%s has profile %s, want pvc-policy", status, e.Profile)
			}
		}
	}
}

// Even an automatically repairable status is not authorization: absent live
// evidence, model validation and verification, the initial plan must decline.
func TestInventoryFindingsCannotApplyWithoutProof(t *testing.T) {
	for _, e := range readInventory(t).Findings {
		t.Run(e.Source+"/"+e.Status, func(t *testing.T) {
			f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "unproven", Namespace: "test", Status: e.Status}
			p := fix.BuildPlan(f)
			if p.ApplyEligible || p.CanApply {
				t.Fatalf("status alone authorized a mutation: %+v", p)
			}
		})
	}
}

func statusLiteral(expr ast.Expr) string {
	switch n := expr.(type) {
	case *ast.BasicLit:
		if n.Kind == token.STRING {
			value, _ := strconv.Unquote(n.Value)
			return value
		}
	case *ast.BinaryExpr:
		if n.Op == token.ADD {
			return statusLiteral(n.X)
		}
	case *ast.CallExpr:
		if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "firstNonEmpty" {
			for _, arg := range n.Args {
				if value := statusLiteral(arg); value != "" {
					return value
				}
			}
		}
	}
	return ""
}

func readInventory(t *testing.T) inventory {
	t.Helper()
	data, err := os.ReadFile("remediation.json")
	if err != nil {
		t.Fatal(err)
	}
	var inv inventory
	if err := json.Unmarshal(data, &inv); err != nil {
		t.Fatal(err)
	}
	return inv
}

func checkNames(t *testing.T, kind string, actual, documented []string) {
	t.Helper()
	seen := map[string]bool{}
	for _, name := range documented {
		if seen[name] {
			t.Errorf("duplicate %s %s", kind, name)
		}
		seen[name] = true
	}
	for _, name := range actual {
		if !seen[name] {
			t.Errorf("unreviewed %s %s", kind, name)
		}
		delete(seen, name)
	}
	for name := range seen {
		t.Errorf("stale %s %s", kind, name)
	}
}
