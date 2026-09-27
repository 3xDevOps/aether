package scheduler

import (
	"bytes"
	"path/filepath"
	"slices"
	"testing"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
)

func TestNativeCoordinationLaunchBoundaries(t *testing.T) {
	for _, name := range []string{"omp", "pi", "opencode"} {
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
				if boundary != "interactive" {
					if asset != nil || spec.Env["AETHER_MANAGED_NATIVE_WAKE"] != "" {
						t.Fatalf("%s acquired a native receiver", boundary)
					}
					if boundary == "override" && !slices.Contains(argv, "--no-extensions") {
						t.Fatalf("user disable argument lost: %v", argv)
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
