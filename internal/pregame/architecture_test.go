package pregame

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPackageHasNoLiveStateOrPersistenceDependencies(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(".", entry.Name())
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range file.Imports {
			value, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"/state", "/tracker", "/recommendation", "/storage"} {
				if strings.HasSuffix(value, forbidden) {
					t.Fatalf("%s imports forbidden live/persistent package %q", path, value)
				}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			prefix, ok := selector.X.(*ast.Ident)
			if ok && prefix.Name == "domain" && (selector.Sel.Name == "GameState" || selector.Sel.Name == "Snapshot") {
				t.Fatalf("%s references forbidden live type domain.%s", path, selector.Sel.Name)
			}
			return true
		})
	}
}
