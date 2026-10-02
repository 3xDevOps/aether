package scheduler

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/ptyhost"
)

func TestNativeSilentWorkingHeartbeat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native extension needs a POSIX reporter")
	}
	for _, tc := range []struct {
		name, harness, runner, driver, asset, scenario string
		source                                         []byte
	}{
		{"pi", "pi", "bun", "drive.ts", "status.ts", "heartbeat-pi", agentstatus.PiExtension},
		{"omp", "omp", "bun", "drive.ts", "status.ts", "heartbeat-omp", agentstatus.PiExtension},
		{"opencode-v1", "opencode", "node", "drive-opencode.mjs", "status.js", "v1", agentstatus.OpenCodePlugin},
		{"opencode-v2", "opencode", "node", "drive-opencode.mjs", "status.js", "v2", agentstatus.OpenCodeV2Plugin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, err := exec.LookPath(tc.runner)
			if err != nil {
				t.Skipf("%s is not installed: %v", tc.runner, err)
			}
			dir := t.TempDir()
			logPath := filepath.Join(dir, "reports")
			reporter := filepath.Join(dir, "reporter")
			write := func(name, body string, mode os.FileMode) {
				t.Helper()
				if writeErr := os.WriteFile(filepath.Join(dir, name), []byte(body), mode); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			write("reporter", "#!/bin/sh\nprintf '%s\\n' \"$4\" >> '"+logPath+"'\n", 0o700)
			staged := strings.ReplaceAll(string(tc.source), agentstatus.ReporterCommand, reporter)
			write(tc.asset, staged, 0o600)
			driver, err := os.ReadFile(filepath.Join("..", "agentstatus", "testdata", tc.driver))
			if err != nil {
				t.Fatal(err)
			}
			write(tc.driver, string(driver), 0o600)
			args := []string{"run"}
			if tc.runner == "node" {
				args = []string{"--experimental-vm-modules", "--no-warnings"}
			}
			args = append(args, tc.driver, tc.scenario, logPath)
			cmd := exec.CommandContext(t.Context(), runner, args...)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("native lifecycle: %v\n%s", err, out)
			}
			var checkpoints []struct {
				Phase   string               `json:"phase"`
				Reports []agentstatus.Report `json:"reports"`
			}
			if err := json.Unmarshal(out, &checkpoints); err != nil {
				t.Fatalf("decode native lifecycle: %v\n%s", err, out)
			}

			e := newTestEnv(t, func(cfg *Config) {
				cfg.ServerBinary = fakeServerBinary(t, "#!/bin/sh\necho aether\n")
				cfg.StallThreshold = time.Hour
			})
			coord := &recordingCoordinator{
				fakeCoordinator: fakeCoordinator{root: filepath.Join(dir, "coord")},
				files:           make(map[domain.RunID]map[string][]byte),
			}
			e.sched.UseCoordination(coord, filepath.Join(dir, "runtime", "bin"))
			run, _ := e.launchOn(t, tc.harness)
			for _, checkpoint := range checkpoints {
				if checkpoint.Phase == "work" {
					// Age the existing activity clocks by less than one stall
					// threshold per native callback. Without repeated heartbeats,
					// their cumulative age must eventually park the live run.
					e.sched.mu.Lock()
					entry := e.sched.runs[run.ID]
					entry.startedAt = entry.startedAt.Add(-45 * time.Minute)
					entry.lastWorking = entry.lastWorking.Add(-45 * time.Minute)
					e.sched.mu.Unlock()
				}
				for _, report := range checkpoint.Reports {
					if err := e.sched.ReportAgentState(t.Context(), run.ID, report); err != nil {
						t.Fatalf("%s report: %v", checkpoint.Phase, err)
					}
				}
				e.sched.checkStalls(t.Context())
				stored, err := e.db.GetRun(t.Context(), run.ID)
				if err != nil {
					t.Fatal(err)
				}
				want := domain.RunRunning
				if checkpoint.Phase == "idle" || checkpoint.Phase == "late-message" {
					want = domain.RunNeedsAttention
					if stored.Reason != agentstatus.ReasonIdle {
						t.Fatalf("%s reason = %q, want genuine agent idle", checkpoint.Phase, stored.Reason)
					}
				}
				if stored.Status != want {
					t.Fatalf("%s: run = %s (%s), want %s", checkpoint.Phase, stored.Status, stored.Reason, want)
				}
				if inputs := e.sched.PendingInputs(run.ID); len(inputs) != 0 {
					t.Fatalf("%s fabricated human input: %+v", checkpoint.Phase, inputs)
				}
			}
			if last, _ := e.pty.LastOutput(ptyhost.RunSession(run.ID)); !last.IsZero() {
				t.Fatalf("scenario was not terminal-silent: %v", last)
			}
			if last, _ := e.git.LastFileChange(run.ID); !last.IsZero() {
				t.Fatalf("scenario was not filesystem-silent: %v", last)
			}
		})
	}
}
