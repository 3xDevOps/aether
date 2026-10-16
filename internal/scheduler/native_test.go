package scheduler

import (
	"bytes"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
)

func TestNativeCoordinationLaunchBoundaries(t *testing.T) {
	for _, name := range []string{"claude", "codex", "copilot", "omp", "pi", "opencode"} {
		for _, boundary := range []string{"interactive", "taskless", "headless", "disabled", "unconfigured", "override"} {
			if name == "opencode" && boundary == "interactive" {
				continue // The versioned mount pair is exercised by its registration test.
			}
			t.Run(name+"/"+boundary, func(t *testing.T) {
				t.Parallel()
				e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")))
				dir := t.TempDir()
				coord := &recordingCoordinator{
					fakeCoordinator: fakeCoordinator{root: filepath.Join(dir, "coord")},
					files:           make(map[domain.RunID]map[string][]byte),
				}
				if boundary != "unconfigured" {
					e.sched.UseCoordination(coord, filepath.Join(dir, "bin"), boundary != "disabled")
				}
				task, mode := "review the quoted 'task' without changing it", domain.LaunchTUI
				if boundary == "taskless" {
					task = ""
				}
				if boundary == "headless" {
					mode = domain.LaunchHeadless
				}
				if boundary == "override" {
					e.sched.harnesses[name] = HarnessSpec{TUIArgs: []string{name, "--no-extensions", "{task}"}, HeadlessArgs: []string{name, "-p", "{task}"}}
				}
				run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, task, name, mode)
				if err != nil {
					t.Fatal(err)
				}
				spec := e.rt.byName(string(run.ID)).spec
				argv := spec.Command
				if mode == domain.LaunchTUI {
					argv = tuiHarnessCommand(t, argv)
				}
				asset := coord.file(run.ID, "aether.ts")
				if name == "opencode" {
					asset = coord.file(run.ID, "opencode-v1.js")
				}
				settings := coord.file(run.ID, agentstatus.ClaudeSettingsName)
				if boundary != "interactive" {
					if asset != nil || spec.Env["AETHER_MANAGED_NATIVE_WAKE"] != "" || spec.Env[coordtransport.ManagedHooksEnv] != "" {
						t.Fatalf("%s acquired a native receiver", boundary)
					}
					if bytes.Contains(settings, []byte("hook claude wake")) {
						t.Fatalf("%s wrote the claude wake hook: %s", boundary, settings)
					}
					if name == "claude" && boundary == "taskless" && (!bytes.Equal(settings, agentstatus.ClaudeSettings) || countOf(argv, "--settings") != 1) {
						t.Fatalf("taskless claude settings = %s with argv %v, want the status reporter alone", settings, argv)
					}
					if name == "codex" && slices.ContainsFunc(argv, func(a string) bool { return strings.HasPrefix(a, "hooks.") }) {
						t.Fatalf("%s loaded the codex hooks: %v", boundary, argv)
					}
					if boundary == "override" && !slices.Contains(argv, "--no-extensions") {
						t.Fatalf("user disable argument lost: %v", argv)
					}
					return
				}
				if name == "copilot" {
					want, readErr := coordhooks.Files.ReadFile("copilot.json")
					if readErr != nil {
						t.Fatal(readErr)
					}
					if !bytes.Equal(coord.file(run.ID, "copilot-hooks.json"), want) || coord.file(run.ID, "plugin.json") == nil {
						t.Fatal("copilot run was not staged the plugin manifest and hooks")
					}
					if i := slices.Index(argv, "--plugin-dir"); countOf(argv, "--plugin-dir") != 1 || argv[i+1] != coordtransport.MountDir {
						t.Fatalf("copilot argv = %v, want one --plugin-dir %s", argv, coordtransport.MountDir)
					}
					if spec.Env[coordtransport.ManagedHooksEnv] != "1" {
						t.Fatalf("copilot run lacks %s", coordtransport.ManagedHooksEnv)
					}
					return
				}
				if name == "claude" {
					if want := claudeNativeSettings(t); !bytes.Equal(settings, want) {
						t.Fatalf("claude settings = %s, want %s", settings, want)
					}
					if i := slices.Index(argv, "--settings"); countOf(argv, "--settings") != 1 || argv[i+1] != coordtransport.MountDir+"/"+agentstatus.ClaudeSettingsName {
						t.Fatalf("claude argv = %v, want one --settings pointing at the merged document", argv)
					}
					if spec.Env[coordtransport.ManagedHooksEnv] != "1" {
						t.Fatalf("claude run lacks %s", coordtransport.ManagedHooksEnv)
					}
					return
				}
				if spec.Env[coordtransport.ManagedHooksEnv] != "1" {
					t.Fatalf("%s run lacks %s", name, coordtransport.ManagedHooksEnv)
				}
				if name == "codex" {
					hooks := 0
					for i, a := range argv {
						if i > 0 && argv[i-1] == "-c" && strings.HasPrefix(a, "hooks.") {
							hooks++
						}
					}
					if hooks != 4 || !slices.Contains(argv, agentstatus.CodexNotifySetting) {
						t.Fatalf("codex argv = %v, want 4 hooks.* overrides next to the notify setting", argv)
					}
					return
				}
				want, err := coordhooks.Files.ReadFile(name + ".ts")
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(asset, want) {
					t.Fatal("run was provisioned with the wrong harness's native adapter")
				}
				mount, ok := mountFor(spec, coordtransport.MountDir)
				if !ok || !mount.ReadOnly {
					t.Fatalf("native integration has no read-only run asset mount: %+v", spec.Mounts)
				}
				wantPath := coordtransport.MountDir + "/aether.ts"
				if name == "omp" {
					wantPath = coordtransport.MountDir
				}
				if len(argv) < 2 || argv[len(argv)-2] != "-e" || argv[len(argv)-1] != wantPath {
					t.Fatalf("native loader path = %v; want directory-filtered OMP or pi file path %s", argv, wantPath)
				}
				if !bytes.Equal(coord.file(run.ID, agentstatus.PiExtensionName), agentstatus.PiExtension) {
					t.Fatal("native inbox adapter replaced the independent status reporter")
				}
			})
		}
	}
}

func countOf(argv []string, arg string) int {
	n := 0
	for _, a := range argv {
		if a == arg {
			n++
		}
	}
	return n
}
