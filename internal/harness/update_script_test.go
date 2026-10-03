package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/agentstatus"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "package-exchange" {
		if len(os.Args) != 4 {
			os.Exit(2)
		}
		if err := ExchangePackages(os.Args[2], os.Args[3]); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const stubNPM = `#!/bin/sh
case "$1" in
view) echo 2.0.0 ;;
install)
	while [ $# -gt 0 ]; do
		[ "$1" = --prefix ] && { shift; prefix=$1; }
		last=$1
		shift
	done
	dir="$prefix/lib/node_modules/${last%@*}"
	mkdir -p "$dir/bin" || exit 1
	echo "${last##*@}" > "$dir/package.json"
	cp "$HOME/agent-template" "$dir/bin/$AETHER_TEST_AGENT"
	chmod +x "$dir/bin/$AETHER_TEST_AGENT" ;;
esac
`

func TestNPMUpdateScripts(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("updates run in Linux containers")
	}
	testBinary, executableErr := os.Executable()
	if executableErr != nil {
		t.Fatal(executableErr)
	}
	for _, tc := range []struct {
		name, pkg, versionFormat string
	}{
		{"codex", "@openai/codex", "codex-cli %s"},
		{"pi", "@earendil-works/pi-coding-agent", "%s"},
	} {
		profile, _ := Lookup(tc.name)
		fixture := func(t *testing.T, installed string) string {
			t.Helper()
			home := t.TempDir()
			bin := filepath.Join(home, ".local", "bin")
			stubs := filepath.Join(home, "stubs")
			pkgDir := filepath.Join(home, ".local", "lib", "node_modules", tc.pkg)
			for _, dir := range []string{bin, stubs, filepath.Dir(pkgDir)} {
				if mkdirErr := os.MkdirAll(dir, 0o755); mkdirErr != nil {
					t.Fatal(mkdirErr)
				}
			}
			write := func(path, body string) {
				t.Helper()
				if writeErr := os.WriteFile(path, []byte(body), 0o755); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			exe := fmt.Sprintf("#!/bin/sh\nprintf '%s\\n' \"$(cat %q)\"\n", tc.versionFormat, filepath.Join(pkgDir, "package.json"))
			write(filepath.Join(home, "agent-template"), exe)
			write(filepath.Join(stubs, "npm"), stubNPM)
			write(filepath.Join(stubs, "mv"), `#!/bin/sh
/usr/bin/mv "$@" || exit
"$HOME/.local/bin/$AETHER_TEST_AGENT" --version >> "$HOME/launches"
`)
			helper := filepath.Join(stubs, "server")
			write(helper, fmt.Sprintf(`#!/bin/sh
stop_at() {
	[ "$(cat "$HOME/stop" 2>/dev/null)" = "$1" ] || return 0
	rm "$HOME/stop"
	kill -KILL "$PPID"
	exit 0
}
"$HOME/.local/bin/$AETHER_TEST_AGENT" --version >> "$HOME/launches" || exit
stop_at before
[ ! -e "$HOME/fail-exchange" ] || rm -rf "$3"
%q "$@" || exit
"$HOME/.local/bin/$AETHER_TEST_AGENT" --version >> "$HOME/launches" || exit
stop_at after
`, testBinary))
			write(filepath.Join(home, "update"), strings.ReplaceAll(profile.UpdateScript, agentstatus.ReporterCommand, helper))
			if installed == "" {
				write(filepath.Join(bin, tc.name), "#!/bin/sh\necho standalone\n")
			} else {
				if mkdirErr := os.MkdirAll(filepath.Join(pkgDir, "bin"), 0o755); mkdirErr != nil {
					t.Fatal(mkdirErr)
				}
				write(filepath.Join(pkgDir, "package.json"), installed+"\n")
				write(filepath.Join(pkgDir, "bin", tc.name), exe)
				if linkErr := os.Symlink("../lib/node_modules/"+tc.pkg+"/bin/"+tc.name, filepath.Join(bin, tc.name)); linkErr != nil {
					t.Fatal(linkErr)
				}
			}
			return home
		}
		update := func(t *testing.T, home string) ([]byte, error) {
			t.Helper()
			cmd := exec.CommandContext(t.Context(), "/bin/sh", filepath.Join(home, "update"))
			cmd.Env = []string{
				"HOME=" + home,
				"PATH=" + filepath.Join(home, ".local", "bin") + ":" + filepath.Join(home, "stubs") + ":/usr/bin:/bin",
				"AETHER_TEST_AGENT=" + tc.name,
			}
			return cmd.CombinedOutput()
		}
		version := func(t *testing.T, home, want string) {
			t.Helper()
			cmd := exec.CommandContext(t.Context(), filepath.Join(home, ".local", "bin", tc.name), "--version")
			out, commandErr := cmd.CombinedOutput()
			if commandErr != nil || strings.TrimSpace(string(out)) != want {
				t.Fatalf("launch = %v, %q; want %q", commandErr, out, want)
			}
		}
		t.Run(tc.name+"/outdated", func(t *testing.T) {
			home := fixture(t, "1.0.0")
			version(t, home, fmt.Sprintf(tc.versionFormat, "1.0.0"))
			if out, updateErr := update(t, home); updateErr != nil {
				t.Fatalf("update: %v\n%s", updateErr, out)
			}
			version(t, home, fmt.Sprintf(tc.versionFormat, "2.0.0"))
		})
		t.Run(tc.name+"/not npm", func(t *testing.T) {
			home := fixture(t, "")
			version(t, home, "standalone")
			if out, updateErr := update(t, home); updateErr == nil {
				t.Fatalf("non-npm installation was accepted: %s", out)
			}
			version(t, home, "standalone")
		})
		t.Run(tc.name+"/legacy interrupted move", func(t *testing.T) {
			home := fixture(t, "1.0.0")
			previous := filepath.Join(home, ".local", "lib", "."+tc.name+"-update.interrupted", "previous")
			if mkdirErr := os.MkdirAll(filepath.Dir(previous), 0o755); mkdirErr != nil {
				t.Fatal(mkdirErr)
			}
			pkgDir := filepath.Join(home, ".local", "lib", "node_modules", tc.pkg)
			if renameErr := os.Rename(pkgDir, previous); renameErr != nil {
				t.Fatal(renameErr)
			}
			if out, updateErr := update(t, home); updateErr != nil {
				t.Fatalf("recover interrupted update: %v\n%s", updateErr, out)
			}
			version(t, home, fmt.Sprintf(tc.versionFormat, "2.0.0"))
		})
		t.Run(tc.name+"/exchange failure", func(t *testing.T) {
			home := fixture(t, "1.0.0")
			if writeErr := os.WriteFile(filepath.Join(home, "fail-exchange"), nil, 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
			if out, updateErr := update(t, home); updateErr == nil {
				t.Fatalf("missing staged package was accepted: %s", out)
			}
			version(t, home, fmt.Sprintf(tc.versionFormat, "1.0.0"))
		})
		for _, point := range []string{"before", "after"} {
			t.Run(tc.name+"/interrupted "+point+" exchange", func(t *testing.T) {
				home := fixture(t, "1.0.0")
				if writeErr := os.WriteFile(filepath.Join(home, "stop"), []byte(point), 0o600); writeErr != nil {
					t.Fatal(writeErr)
				}
				if out, updateErr := update(t, home); updateErr == nil {
					t.Fatalf("update was not interrupted: %s", out)
				}
				want := "1.0.0"
				if point == "after" {
					want = "2.0.0"
				}
				version(t, home, fmt.Sprintf(tc.versionFormat, want))
				if out, updateErr := update(t, home); updateErr != nil {
					t.Fatalf("retry interrupted update: %v\n%s", updateErr, out)
				}
				version(t, home, fmt.Sprintf(tc.versionFormat, "2.0.0"))
			})
		}
	}
}
