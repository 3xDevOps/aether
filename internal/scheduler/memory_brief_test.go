package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/memory"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// withTeamMemory serves launch briefs from real team memory over the env's
// store.
func withTeamMemory(t *testing.T) func(*Config) {
	return func(cfg *Config) {
		log, err := events.OpenSQLiteLog(filepath.Join(t.TempDir(), "events.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = log.Close() })
		notes, ok := cfg.Store.(memory.Store)
		if !ok {
			t.Fatal("the test store does not hold team memory")
		}
		mem, err := memory.New(memory.Config{Store: notes, Bus: cfg.Bus, Log: log})
		if err != nil {
			t.Fatal(err)
		}
		cfg.Memory = mem
	}
}

// teamMemory is what a seeded brief must and must not show.
type teamMemory struct {
	pin, recentRun, agentNote string
}

// seedTeamMemory pins a convention, and records a run that wrote an
// unpinned agent note and finished an hour ago.
func seedTeamMemory(t *testing.T, e *testEnv) teamMemory {
	t.Helper()
	ctx := t.Context()
	mem := e.cfg.Memory.(*memory.Service)
	seed := teamMemory{
		pin:       "convention (Ada): Use bun in web/; never npm.",
		recentRun: "- Ada codex completed `aether/run-fix-reattach`, 1h: ",
		agentNote: "The reattach race is DNS.",
	}
	if _, err := mem.Add(ctx, e.member.ID, protocol.MemoryAddParams{
		WorkspaceID: string(e.ws.ID), Kind: "convention", Text: "Use bun in web/; never npm.", Pin: true,
	}); err != nil {
		t.Fatal(err)
	}
	done := &domain.Run{WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "Fix the reattach lease race",
		Harness: "codex", Mode: domain.LaunchTUI, Status: domain.RunRunning, Branch: "aether/run-fix-reattach"}
	if err := e.db.CreateRun(ctx, done); err != nil {
		t.Fatal(err)
	}
	add, err := json.Marshal(protocol.MemoryAddParams{Kind: "gotcha", Text: seed.agentNote})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.HandleAgent(ctx, done.ID, protocol.MethodMemoryAdd, add); err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC().Add(-time.Hour - time.Minute)
	if _, err := e.db.UpdateRunStatus(ctx, done.ID, domain.RunCompleted, "", "", nil, &finished); err != nil {
		t.Fatal(err)
	}
	return seed
}

func (m teamMemory) check(t *testing.T, where, text string) {
	t.Helper()
	if rule := strings.Index(text, coAuthorInstruction); rule < 0 || strings.Index(text, "<team-memory") < rule {
		t.Fatalf("%s %q lacks the co-author rule before the brief", where, text)
	}
	if !strings.Contains(text, m.pin) || !strings.Contains(text, m.recentRun) {
		t.Fatalf("%s %q lacks the pinned note or the recent run", where, text)
	}
	if strings.Contains(text, m.agentNote) {
		t.Fatalf("%s %q carries an unpinned agent note", where, text)
	}
}

// argAfter is the argument following flag in argv, or the first one that
// starts with prefix when flag is empty.
func argAfter(argv []string, flag, prefix string) string {
	for i, arg := range argv {
		if flag != "" && arg == flag && i+1 < len(argv) {
			return argv[i+1]
		}
		if flag == "" && strings.HasPrefix(arg, prefix) {
			return arg
		}
	}
	return ""
}

func TestEveryLaunchCarriesTheTeamMemoryBrief(t *testing.T) {
	for _, name := range []string{"claude", "codex", "copilot", "opencode", "gemini", "cursor"} {
		t.Run(name, func(t *testing.T) {
			e := newTestEnv(t, func(cfg *Config) {
				withServerBinary(fakeServerBinary(t, "brief cli"))(cfg)
				withTeamMemory(t)(cfg)
			})
			coord := &recordingCoordinator{
				fakeCoordinator: fakeCoordinator{root: filepath.Join(t.TempDir(), "coord")},
				files:           make(map[domain.RunID]map[string][]byte),
			}
			e.sched.UseCoordination(coord, filepath.Join(t.TempDir(), "bin"), true)
			seed := seedTeamMemory(t, e)
			const task = "fix the login"
			run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, task, name, domain.LaunchTUI)
			if err != nil {
				t.Fatal(err)
			}
			argv := e.rt.byName(string(run.ID)).spec.Command
			profile, _ := harness.Lookup(name)
			var got, where string
			switch name {
			case "claude":
				got, where = argAfter(argv, "--append-system-prompt", ""), "--append-system-prompt"
			case "codex":
				where = "developer_instructions"
				if got, err = harness.ConfiguredInstructions([]byte(argAfter(argv, "", "developer_instructions="))); err != nil {
					t.Fatalf("decode %s from %v: %v", where, argv, err)
				}
			case "copilot", "opencode":
				got, where = string(coord.file(run.ID, profile.InstructionsFile)), profile.InstructionsFile
			case "gemini":
				got, where = argAfter(argv, "", "--prompt-interactive="+task), "task prompt"
			case "cursor":
				got, where = argAfter(argv, "", task), "task prompt"
			}
			seed.check(t, where, got)
		})
	}
}

// A launch that carries no role card carries no brief either.
func TestTasklessPromptOnlyLaunchGetsNoBrief(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		withServerBinary(fakeServerBinary(t, "brief cli"))(cfg)
		withTeamMemory(t)(cfg)
	})
	withCoordination(t, e)
	seedTeamMemory(t, e)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "gemini", domain.LaunchTUI)
	if err != nil {
		t.Fatal(err)
	}
	if argv := e.rt.byName(string(run.ID)).spec.Command; slices.ContainsFunc(argv, func(arg string) bool { return strings.Contains(arg, "<team-memory") }) {
		t.Fatalf("taskless gemini launch %v carries a brief", argv)
	}
}

func TestEnhancedSystemPromptCarriesTheTeamMemoryBrief(t *testing.T) {
	e, rt := newACPEnv(t, withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")), withTeamMemory(t))
	withCoordination(t, e)
	seed := seedTeamMemory(t, e)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	meta, _ := rt.all()[0].wire.sessionNew(t)
	system, _ := meta["systemPrompt"].(map[string]any)
	got, _ := system["append"].(string)
	seed.check(t, "session/new systemPrompt.append", got)
}

type failingMemory struct{}

func (failingMemory) Brief(context.Context, domain.WorkspaceID, domain.MemberID, domain.RunID) (string, error) {
	return "", errors.New("memory.brief: database is locked")
}

func TestBriefErrorFailsTheLaunch(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		withServerBinary(fakeServerBinary(t, "brief cli"))(cfg)
		cfg.Memory = failingMemory{}
	})
	withCoordination(t, e)
	for _, name := range []string{"claude", "gemini"} {
		_, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "fix the login", name, domain.LaunchTUI)
		if err == nil || !strings.Contains(err.Error(), "build the team memory brief: memory.brief: database is locked") {
			t.Fatalf("%s launch error = %v, want the brief's error", name, err)
		}
	}
}
