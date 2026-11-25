package orbtr

// The contract against the platform's source: for every schema whose x-orbtr-source names a Go struct in
// cortex, the struct's JSON fields and the schema's properties are the same set (a schema marked
// x-orbtr-source-subset may hold fewer; x-orbtr-source-adds names properties another type adds). It runs
// when ORBTR_CORTEX_DIR names a cortex checkout, and fails when fewer than minChecked schemas could be
// compared, so a moved tree cannot pass by checking nothing.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ORBTR/sdk-go/internal/gen"
)

const minChecked = 20

type goPackage struct {
	files []*ast.File
}

func loadGoPackage(t *testing.T, dir string) *goPackage {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	p := &goPackage{}
	fset := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		p.files = append(p.files, f)
	}
	return p
}

// structType finds a struct type of the given name anywhere in the package, function bodies included.
func (p *goPackage) structType(name string) *ast.StructType {
	var found *ast.StructType
	for _, f := range p.files {
		ast.Inspect(f, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok && ts.Name.Name == name {
				if st, ok := ts.Type.(*ast.StructType); ok {
					found = st
				}
			}
			return found == nil
		})
	}
	return found
}

// jsonFields is a struct's JSON field names; an embedded struct of the same package adds its own.
func (p *goPackage) jsonFields(st *ast.StructType) (fields []string, unresolved []string) {
	for _, f := range st.Fields.List {
		name := ""
		if f.Tag != nil {
			tag, _ := strconv.Unquote(f.Tag.Value)
			name = strings.Split(reflect.StructTag(tag).Get("json"), ",")[0]
			if name == "-" {
				continue
			}
		}
		if len(f.Names) == 0 && name == "" {
			if id, ok := f.Type.(*ast.Ident); ok {
				if inner := p.structType(id.Name); inner != nil {
					more, un := p.jsonFields(inner)
					fields = append(fields, more...)
					unresolved = append(unresolved, un...)
					continue
				}
			}
			unresolved = append(unresolved, types(f.Type))
			continue
		}
		if name == "" {
			for _, n := range f.Names {
				if n.IsExported() {
					fields = append(fields, n.Name)
				}
			}
			continue
		}
		fields = append(fields, name)
	}
	return fields, unresolved
}

func types(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return types(v.X) + "." + v.Sel.Name
	case *ast.StarExpr:
		return "*" + types(v.X)
	}
	return "?"
}

func TestContractMatchesCortex(t *testing.T) {
	root := os.Getenv("ORBTR_CORTEX_DIR")
	if root == "" {
		t.Skip("ORBTR_CORTEX_DIR is not set")
	}
	data, err := os.ReadFile("contract/orbtr-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := gen.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	pkgs := map[string]*goPackage{}
	checked := 0
	var notStructs []string
	for _, name := range c.SchemaOrder {
		s := c.Schemas[name]
		parts := strings.Fields(s.Source)
		if len(parts) != 2 {
			notStructs = append(notStructs, name)
			continue
		}
		dir, typ := parts[0], parts[1]
		p := pkgs[dir]
		if p == nil {
			p = loadGoPackage(t, filepath.Join(root, dir))
			pkgs[dir] = p
		}
		st := p.structType(typ)
		if st == nil {
			notStructs = append(notStructs, name)
			continue
		}
		fields, unresolved := p.jsonFields(st)
		if len(unresolved) > 0 {
			t.Errorf("%s: %s %s embeds %v, which this check cannot read", name, dir, typ, unresolved)
			continue
		}
		checked++
		inGo := map[string]bool{}
		for _, f := range fields {
			inGo[f] = true
		}
		adds := map[string]bool{}
		for _, a := range s.SourceAdds {
			adds[a] = true
		}
		var missing, extra []string
		for _, p := range s.PropOrder {
			if !inGo[p] && !adds[p] {
				missing = append(missing, p)
			}
		}
		for _, f := range fields {
			if s.Properties[f] == nil && !s.SourceSubset {
				extra = append(extra, f)
			}
		}
		sort.Strings(extra)
		if len(missing) > 0 {
			t.Errorf("%s: properties %v are not fields of %s %s", name, missing, dir, typ)
		}
		if len(extra) > 0 {
			t.Errorf("%s: %s %s has fields %v the contract does not", name, dir, typ, extra)
		}
	}
	t.Logf("compared %d schemas with their cortex structs; %d name a handler or have no struct: %v", checked, len(notStructs), notStructs)
	if checked < minChecked {
		t.Fatalf("only %d schemas could be compared; want at least %d", checked, minChecked)
	}
}
