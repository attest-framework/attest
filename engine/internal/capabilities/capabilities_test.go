package capabilities

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	pythonAdaptersDir = "../../../sdks/python/src/attest/adapters"
	tsAdaptersDir     = "../../../sdks/typescript/packages/core/src/adapters"
)

// tsHelperFiles are files in the TypeScript adapters directory that are not
// adapters.
var tsHelperFiles = map[string]bool{"index": true, "provider-trace": true}

func TestBuild_AssertionTypesMatchPkgTypesConstants(t *testing.T) {
	// Arrange
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "../../pkg/types/assertion.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range vs.Names {
			if !strings.HasPrefix(name.Name, "Type") || i >= len(vs.Values) {
				continue
			}
			if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				v, _ := strconv.Unquote(lit.Value)
				declared[v] = true
			}
		}
		return true
	})

	// Act
	doc, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, l := range doc.Layers {
		for _, a := range l.AssertionTypes {
			listed[a] = true
		}
	}

	// Assert
	for v := range declared {
		if !listed[v] {
			t.Errorf("assertion type %q is declared in pkg/types but belongs to no layer in the capabilities document", v)
		}
	}
	for v := range listed {
		if !declared[v] {
			t.Errorf("capabilities lists assertion type %q that pkg/types does not declare", v)
		}
	}
}

func TestBuild_LayersAreSixEvaluatedPlusPlugins(t *testing.T) {
	doc, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for _, l := range doc.Layers {
		ids = append(ids, l.ID)
	}
	want := []int{1, 2, 3, 4, 5, 6, 8}
	if len(ids) != len(want) {
		t.Fatalf("layer ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("layer ids = %v, want %v", ids, want)
		}
	}
	if last := doc.Layers[len(doc.Layers)-1]; last.Name != "Plugins" || len(last.AssertionTypes) != 1 || last.AssertionTypes[0] != "plugin" {
		t.Errorf("last layer = %+v, want the plugins entry", last)
	}
}

func TestBuild_AdaptersMatchSDKDirectories(t *testing.T) {
	doc, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		dir      string
		ext      string
		skip     func(stem string) bool
		adapters []Adapter
	}{
		{"python", pythonAdaptersDir, ".py", func(s string) bool { return strings.HasPrefix(s, "_") }, doc.Adapters.Python},
		{"typescript", tsAdaptersDir, ".ts", func(s string) bool { return tsHelperFiles[s] }, doc.Adapters.TypeScript},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			entries, err := os.ReadDir(c.dir)
			if err != nil {
				t.Fatal(err)
			}
			var onDisk []string
			for _, e := range entries {
				stem := strings.TrimSuffix(e.Name(), c.ext)
				if e.IsDir() || filepath.Ext(e.Name()) != c.ext || c.skip(stem) {
					continue
				}
				onDisk = append(onDisk, stem)
			}
			var listed []string
			for _, a := range c.adapters {
				listed = append(listed, a.Module)
				switch a.Maturity {
				case MaturityStable, MaturityBeta, MaturityExperimental:
				default:
					t.Errorf("adapter %s has maturity %q", a.Module, a.Maturity)
				}
			}
			sort.Strings(onDisk)
			sort.Strings(listed)
			if strings.Join(onDisk, ",") != strings.Join(listed, ",") {
				t.Errorf("adapters in data.json = %v, adapter files on disk = %v", listed, onDisk)
			}
		})
	}
}

func TestBuild_FeaturesHaveKnownMaturity(t *testing.T) {
	doc, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range doc.Features {
		switch f.Maturity {
		case MaturityStable, MaturityBeta, MaturityExperimental:
		default:
			t.Errorf("feature %q has maturity %q", f.Name, f.Maturity)
		}
	}
	for _, tier := range []string{MaturityStable, MaturityBeta, MaturityExperimental} {
		if doc.MaturityDefinitions[tier] == "" {
			t.Errorf("no definition for maturity %q", tier)
		}
	}
}

func TestBuild_LayerCostClassification(t *testing.T) {
	// Arrange
	want := map[string]struct {
		cost          string
		deterministic bool
	}{
		"Schema validation":           {"free", true},
		"Constraint checks":           {"free", true},
		"Trace and behavioral checks": {"free", true},
		"Content pattern matching":    {"free", true},
		"Embedding similarity":        {"provider_dependent", false},
		"LLM-as-judge":                {"provider_dependent", false},
		"Plugins":                     {"plugin_defined", false},
	}

	// Act
	doc, err := Build()

	// Assert
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Layers) != len(want) {
		t.Fatalf("%d layers, want %d", len(doc.Layers), len(want))
	}
	for _, l := range doc.Layers {
		w, ok := want[l.Name]
		if !ok {
			t.Errorf("unexpected layer %q", l.Name)
			continue
		}
		if l.Cost != w.cost || l.Deterministic != w.deterministic {
			t.Errorf("%s: cost %q deterministic %t, want %q %t", l.Name, l.Cost, l.Deterministic, w.cost, w.deterministic)
		}
	}
}
