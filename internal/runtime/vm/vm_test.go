package vm

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
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

func TestVMForRoutesEachKindToItsOwnVM(t *testing.T) {
	for key, want := range map[string]string{
		"terminal:m1":              "terminal-m1",
		"assistant:m1":             "assistant-m1",
		"assistant-install:m1":     "assistant-install-m1",
		"harness-update-m1-claude": "harness-update-m1-claude",
		"run-a":                    "",
		"aether-agent-update-x":    "",
	} {
		name, opts, ok := vmFor(runtime.Spec{CreationKey: key})
		if ok != (want != "") || name != want && ok {
			t.Errorf("vmFor(%q) = %q, %v", key, name, ok)
		}
		if opts.KeepRoot != (key == "terminal:m1") {
			t.Errorf("vmFor(%q) keeps its root disk: %v", key, opts.KeepRoot)
		}
		if (opts.Lifetime > 0) != strings.HasPrefix(key, "harness-update-") {
			t.Errorf("vmFor(%q) has a lifetime of %s", key, opts.Lifetime)
		}
	}
}

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
			if container[m.Type.In(j)] && !declared[m.Name] {
				t.Errorf("Runtime does not route %s, which takes a %s", m.Name, m.Type.In(j))
			}
		}
	}
	if !declared["FindByCreationKey"] || !declared["ContainerStats"] || !declared["Capacity"] {
		t.Error("Runtime must also route FindByCreationKey, ContainerStats and Capacity")
	}
}

func TestMemberKindMatchesOnlyTheseMembersKinds(t *testing.T) {
	members := []string{"mem-a", "01m2w6cky0mwwgxmr2sdgev2gs"}
	for key, want := range map[string]bool{
		"terminal:mem-a":                      true,
		"terminal:01m2w6cky0mwwgxmr2sdgev2gs": true,
		"assistant:mem-a":                     true,
		"assistant-install:mem-a":             true,
		"harness-update-mem-a-claude":         true,
		"terminal:mem-b":                      false,
		"harness-update-mem-b-claude":         false,
		"mem-a":                               false,
		"run-mem-a":                           false,
	} {
		if got := memberKind(key, members); got != want {
			t.Errorf("memberKind(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestDestroyLeavesAStoppedDockerTerminal(t *testing.T) {
	r := &Runtime{}
	r.stopped.Store(runtime.ID("0123abcd"), true)
	if err := r.Destroy(t.Context(), "0123abcd"); err != nil {
		t.Fatalf("Destroy of a Docker terminal the server stopped = %v; it must leave it for a server switched back to docker", err)
	}
}
