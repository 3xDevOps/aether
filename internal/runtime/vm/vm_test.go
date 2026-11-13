package vm

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/runtime"
)

func TestIsSessionNeedsTheRunIDTwice(t *testing.T) {
	for _, tc := range []struct {
		spec runtime.Spec
		want bool
	}{
		{runtime.Spec{CreationKey: "run-a", Env: map[string]string{"AETHER_RUN_ID": "run-a"}}, true},
		{runtime.Spec{CreationKey: "terminal:m1"}, false},
		{runtime.Spec{CreationKey: "aether-agent-update-x", Env: map[string]string{"AETHER_RUN_ID": "run-a"}}, false},
		{runtime.Spec{Env: map[string]string{"AETHER_RUN_ID": ""}}, false},
	} {
		if got := isSession(tc.spec); got != tc.want {
			t.Errorf("isSession(%+v) = %v", tc.spec, got)
		}
	}
}

// dockerOnly takes IDs that only Docker ever hands out.
var dockerOnly = []string{"DictationState", "InspectBrowser"}

// TestEveryContainerMethodIsRouted fails when Docker gains a method that
// takes a container, unless Runtime routes it or it is listed above: a
// promoted method would hand a VM's ID to Docker.
func TestEveryContainerMethodIsRouted(t *testing.T) {
	declared := map[string]bool{}
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil {
				continue
			}
			if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok && star.X.(*ast.Ident).Name == "Runtime" {
				declared[fn.Name.Name] = true
			}
		}
	}
	container := map[reflect.Type]bool{
		reflect.TypeFor[runtime.ID](): true, reflect.TypeFor[runtime.ExecIdentity](): true, reflect.TypeFor[runtime.BrowserSpec](): true,
		reflect.TypeFor[runtime.Spec](): true,
	}
	docker := reflect.TypeFor[*runtime.Docker]()
	for i := range docker.NumMethod() {
		m := docker.Method(i)
		for j := 1; j < m.Type.NumIn(); j++ {
			if container[m.Type.In(j)] && !declared[m.Name] && !slices.Contains(dockerOnly, m.Name) {
				t.Errorf("Runtime does not route %s, which takes a %s", m.Name, m.Type.In(j))
			}
		}
	}
	if !declared["FindByCreationKey"] || !declared["ContainerStats"] || !declared["Capacity"] {
		t.Error("Runtime must also route FindByCreationKey, ContainerStats and Capacity")
	}
}
