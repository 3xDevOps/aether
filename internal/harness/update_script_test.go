package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// stubNPM answers "npm view" with 2.0.0 and makes "npm install -g --prefix
// P <pkg>@<v>" write P/lib/node_modules/<pkg>/package.json holding <v>.
const stubNPM = `#!/bin/sh
echo "$*" >> "$HOME/npm.log"
case "$1" in
view) echo 2.0.0 ;;
install)
	while [ $# -gt 0 ]; do
		[ "$1" = --prefix ] && { shift; prefix=$1; }
		last=$1
		shift
	done
	mkdir -p "$prefix/lib/node_modules/${last%@*}" && echo "${last##*@}" > "$prefix/lib/node_modules/${last%@*}/package.json" ;;
esac
`

// The generated npm update scripts, run by sh against stub npm and agent
// executables: a current install is left alone, an outdated one ends with
// the new package in place and no stage behind, and a CLI npm did not
// install is refused.
func TestNPMUpdateScripts(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	for _, tc := range []struct {
		name, pkg, versionFormat string
	}{
		{"codex", "@openai/codex", "codex-cli %s"},
		{"pi", "@earendil-works/pi-coding-agent", "%s"},
	} {
		p, _ := Lookup(tc.name)
		run := func(t *testing.T, installed string) (string, []byte, error) {
			home := t.TempDir()
			bin := filepath.Join(home, "stubs")
			pkgDir := filepath.Join(home, ".local", "lib", "node_modules", tc.pkg)
			exe := fmt.Sprintf("#!/bin/sh\nprintf '%s\\n' \"$(cat %q)\"\n", tc.versionFormat, filepath.Join(pkgDir, "package.json"))
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			for name, body := range map[string]string{"npm": stubNPM, tc.name: exe} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if installed != "" {
				if err := os.MkdirAll(pkgDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(installed+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("sh", "-c", p.UpdateScript)
			cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}
			out, err := cmd.CombinedOutput()
			return home, out, err
		}
		npmLog := func(home string) string {
			log, _ := os.ReadFile(filepath.Join(home, "npm.log"))
			return string(log)
		}
		t.Run(tc.name+"/current", func(t *testing.T) {
			home, out, err := run(t, "2.0.0")
			if err != nil {
				t.Fatalf("update: %v\n%s", err, out)
			}
			if log := npmLog(home); strings.Contains(log, "install") {
				t.Fatalf("current install was reinstalled: %s", log)
			}
		})
		t.Run(tc.name+"/outdated", func(t *testing.T) {
			home, out, err := run(t, "1.0.0")
			if err != nil {
				t.Fatalf("update: %v\n%s", err, out)
			}
			got, _ := os.ReadFile(filepath.Join(home, ".local", "lib", "node_modules", tc.pkg, "package.json"))
			if string(got) != "2.0.0\n" {
				t.Fatalf("installed package.json = %q, want 2.0.0", got)
			}
			entries, _ := os.ReadDir(filepath.Join(home, ".local", "lib"))
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			if !slices.Equal(names, []string{"node_modules"}) {
				t.Fatalf("~/.local/lib = %v, want the stage removed", names)
			}
			if tc.name == "pi" && !strings.Contains(npmLog(home), "--ignore-scripts") {
				t.Fatalf("pi install must pass --ignore-scripts like its install script: %s", npmLog(home))
			}
		})
		t.Run(tc.name+"/not npm", func(t *testing.T) {
			_, out, err := run(t, "")
			want := tc.name + " in ~/.local/bin was not installed with npm, so Aether cannot update it"
			if err == nil || !strings.Contains(string(out), want) {
				t.Fatalf("update = %v, %q; want exit 1 with %q", err, out, want)
			}
		})
	}
}
