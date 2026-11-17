package envname

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/coordtransport"
)

// unconverted are the AETHER_ variables still read under their old name
// alone, each with the reason.
var unconverted = map[string]string{
	"AETHER_SHELL_EXE":                                      "the desktop shell names itself to the gateway it spawns; an inherited CLOUDFIELD_SHELL_EXE must not win",
	"AETHER_DEVEXEC_CONTROL_GROUP":                          "set by the runtime for one helper exec; a CLOUDFIELD_ twin inherited from an image or workspace would share the helper's state with another group",
	"AETHER_BROWSER_CREATION_KEY@images/browser/server.mjs": "set by the server for the browser companion it starts",
	"AETHER_RELEASE_SIGNING_KEY":                            "a secret the release workflows set; no person types it",
	"AETHER_NATIVE_HOOK_OWNER":                              "set and read by the same in-process hook plugin",
	"AETHER_MANAGED_NATIVE_WAKE":                            "set by the server for its own hook plugin",
	"AETHER_TTY":                                            "test seam of install-test.sh",
	"AETHER_UNIT_PATH":                                      "test seam of install-test.sh",
	"AETHER_SYSTEM_BIN_DIR":                                 "test seam of install-test.sh",
	"AETHER_SYSBOX_BASE_URL":                                "test seam of install-sysbox-test.sh",
	"AETHER_SYSBOX_DAEMON_JSON":                             "test seam of install-sysbox-test.sh",
	"AETHER_SYSBOX_MGR_DROPIN":                              "test seam of install-sysbox-test.sh",
	"AETHER_LATEST_URL":                                     "test seam of install-test.ps1",
	"AETHER_API_URL":                                        "test seam of install-test.ps1",
	"AETHER_BIN@scripts/install.ps1":                        "saved and restored around the build install.ps1 runs, not read as a setting",
}

// passesOSGetenv hand os.Getenv on for names other than AETHER_ ones; they
// read their AETHER_ names through envname.Get.
var passesOSGetenv = []string{"internal/coordcli/hook_install.go", "internal/envname/envname.go"}

// skipped are scripts that are not part of this check: tests, smoke runs,
// and edge-update.sh, whose variables a later change converts.
func skipped(rel string) bool {
	base := filepath.Base(rel)
	return strings.HasSuffix(rel, "_test.go") || strings.Contains(base, "-test.") ||
		strings.Contains(base, "-smoke.") || strings.HasSuffix(base, ".test.js") ||
		strings.HasSuffix(base, ".test.ts") || strings.HasSuffix(base, ".spec.ts") ||
		rel == "scripts/edge-update.sh"
}

var (
	shellRead  = regexp.MustCompile(`\$\{?(AETHER_[A-Z0-9_]+)`)
	psRead     = regexp.MustCompile(`GetEnvironmentVariable\('(AETHER_[A-Z0-9_]+)'|\$env:(AETHER_[A-Z0-9_]+)`)
	jsRead     = regexp.MustCompile(`process\.env\.(AETHER_[A-Z0-9_]+)`)
	kotlinRead = regexp.MustCompile(`getenv\("(AETHER_[A-Z0-9_]+)"`)
)

// TestEveryReaderTakesTheNewName fails on a program that reads an AETHER_
// variable without the CLOUDFIELD_ rule, so a reader added later cannot
// miss it.
func TestEveryReaderTakesTheNewName(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var goFiles []string
	found := map[string][]string{}
	report := func(at, name string) {
		if _, ok := unconverted[name]; ok {
			return
		}
		if _, ok := unconverted[name+"@"+at[:strings.LastIndex(at, ":")]]; ok {
			return
		}
		found[name] = append(found[name], at)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", "docs", "dist", "build":
				return filepath.SkipDir
			}
			return nil
		}
		if skipped(rel) {
			return nil
		}
		var re *regexp.Regexp
		switch filepath.Ext(rel) {
		case ".go":
			goFiles = append(goFiles, path)
			return nil
		case ".sh":
			re = shellRead
		case ".ps1":
			re = psRead
		case ".js", ".mjs", ".ts":
			re = jsRead
		case ".kt":
			re = kotlinRead
		default:
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") || strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			for _, m := range re.FindAllStringSubmatchIndex(line, -1) {
				start, end := m[2], m[3]
				if start < 0 {
					start, end = m[4], m[5]
				}
				name := line[start:end]
				if readsBoth(line[:m[0]], name) || writes(line[m[1]:]) {
					continue
				}
				report(rel+":"+strconv.Itoa(i+1), name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	g := scanGo(t, root, goFiles)
	for name, at := range g.osReads {
		for _, a := range at {
			report(a, name)
		}
	}
	for _, at := range g.passed {
		if !slices.Contains(passesOSGetenv, at[:strings.LastIndex(at, ":")]) {
			t.Errorf("%s hands os.Getenv on as a value; hand on envname.Getenv, so the reader takes CLOUDFIELD_ names", at)
		}
	}
	for name, at := range found {
		t.Errorf("%s is read without its CLOUDFIELD_ name at %s; read it through internal/envname, or add it to unconverted with the reason", name, strings.Join(at, ", "))
	}
	for name, at := range g.sessionSets {
		if g.twinReads[name] && !slices.Contains(coordtransport.SessionEnv[:], name) {
			t.Errorf("%s is set in a session at %s and read with its CLOUDFIELD_ name, but is not in coordtransport.SessionEnv, so a workspace variable %s would override it", name, strings.Join(at, ", "), New(name))
		}
	}
}

// readsBoth reports whether the text before an AETHER_ read already took
// the CLOUDFIELD_ name: ${CLOUDFIELD_X:-${AETHER_X...}} in shell and
// process.env.CLOUDFIELD_X || process.env.AETHER_X in JavaScript.
func readsBoth(before, old string) bool {
	twin := New(old)
	return strings.HasSuffix(before, "${"+twin+":-") ||
		strings.HasSuffix(before, "process.env."+twin+" || ") ||
		strings.HasSuffix(before, "process.env."+twin+" || (")
}

// writes reports whether the text after a match assigns the variable
// rather than reading it.
func writes(after string) bool {
	after = strings.TrimLeft(after, " ")
	return strings.HasPrefix(after, "=") && !strings.HasPrefix(after, "==")
}

// goEnv is what the Go source does with AETHER_ names: where it reads one
// through os.Getenv or os.LookupEnv, which it reads through this package,
// and which a package that builds a session's environment sets.
type goEnv struct {
	osReads     map[string][]string
	twinReads   map[string]bool
	sessionSets map[string][]string
	// passed are the places os.Getenv or os.LookupEnv is handed on as a
	// value, so a reader can bypass this package's rule.
	passed []string
}

// sessionBuilders are the packages that set variables in a session's
// environment.
var sessionBuilders = []string{"internal/scheduler/", "internal/harness/", "internal/assistant/", "internal/runtime/"}

// scanGo resolves names given as literals or as constants declared
// anywhere in the repository.
func scanGo(t *testing.T, root string, paths []string) goEnv {
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(paths))
	consts := map[string]string{}
	for _, path := range paths {
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						if v, ok := aetherLiteral(vs.Values[i]); ok {
							consts[name.Name] = v
						}
					}
				}
			}
		}
	}
	resolve := func(e ast.Expr) (string, bool) {
		if v, ok := aetherLiteral(e); ok {
			return v, true
		}
		switch e := e.(type) {
		case *ast.Ident:
			v, ok := consts[e.Name]
			return v, ok
		case *ast.SelectorExpr:
			v, ok := consts[e.Sel.Name]
			return v, ok
		}
		return "", false
	}
	out := goEnv{osReads: map[string][]string{}, twinReads: map[string]bool{}, sessionSets: map[string][]string{}}
	for i, f := range files {
		rel, _ := filepath.Rel(root, paths[i])
		rel = filepath.ToSlash(rel)
		builder := slices.ContainsFunc(sessionBuilders, func(p string) bool { return strings.HasPrefix(rel, p) })
		at := func(n ast.Node) string { return rel + ":" + strconv.Itoa(fset.Position(n.Pos()).Line) }
		set := func(n ast.Node, e ast.Expr) {
			if name, ok := resolve(e); ok && builder {
				out.sessionSets[name] = append(out.sessionSets[name], at(n))
			}
		}
		called := map[ast.Expr]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if isCall(n, "os", "Getenv", "LookupEnv") && !called[n] {
					out.passed = append(out.passed, at(n))
				}
			case *ast.CallExpr:
				called[n.Fun] = true
				if len(n.Args) == 0 {
					return true
				}
				if name, ok := resolve(n.Args[len(n.Args)-1]); ok {
					switch {
					case isCall(n.Fun, "os", "Getenv", "LookupEnv") && len(n.Args) == 1:
						out.osReads[name] = append(out.osReads[name], at(n))
					case isCall(n.Fun, "envname", "Getenv", "Get"):
						out.twinReads[name] = true
					}
				}
			case *ast.AssignStmt:
				for _, lhs := range n.Lhs {
					if index, ok := lhs.(*ast.IndexExpr); ok {
						set(n, index.Index)
					}
				}
			case *ast.KeyValueExpr:
				set(n, n.Key)
			case *ast.BinaryExpr:
				if lit, ok := n.Y.(*ast.BasicLit); ok && n.Op == token.ADD && strings.HasPrefix(lit.Value, "\"=") {
					set(n, n.X)
				}
			case *ast.BasicLit:
				if v, err := strconv.Unquote(n.Value); err == nil && n.Kind == token.STRING && strings.HasPrefix(v, oldPrefix) {
					if name, _, ok := strings.Cut(v, "="); ok {
						set(n, &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(name)})
					}
				}
			}
			return true
		})
	}
	return out
}

func isCall(fun ast.Expr, pkg string, names ...string) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == pkg && slices.Contains(names, sel.Sel.Name)
}

func aetherLiteral(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	return v, err == nil && strings.HasPrefix(v, oldPrefix)
}
