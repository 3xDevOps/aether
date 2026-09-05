package scheduler

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestSaveEnvironmentRequiresRunningTerminal(t *testing.T) {
	e := newTestEnv(t, nil)
	_, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if !errors.Is(err, ErrTerminalNotRunning) {
		t.Fatalf("SaveEnvironment error = %v, want %v", err, ErrTerminalNotRunning)
	}
}

func TestSaveEnvironmentCommitsAndReplacesSavedImage(t *testing.T) {
	e := newTestEnv(t, nil)
	terminal, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	first, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("first SaveEnvironment: %v", err)
	}
	if !regexp.MustCompile(`^aether/member-` + regexp.QuoteMeta(string(e.member.ID)) + `:\d+$`).MatchString(first) {
		t.Fatalf("first image = %q, want member image tag", first)
	}
	member, err := e.db.GetMember(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if member.Image != first {
		t.Fatalf("member image = %q, want %q", member.Image, first)
	}
	calls := e.rt.commitCalls()
	if len(calls) != 1 || calls[0].id != runtime.ID(terminal.ContainerID) || calls[0].tag != first {
		t.Fatalf("commit calls = %+v, want terminal %q and %q", calls, terminal.ContainerID, first)
	}

	time.Sleep(1100 * time.Millisecond)
	second, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("second SaveEnvironment: %v", err)
	}
	if second == first {
		t.Fatalf("second image reused first tag %q", second)
	}
	if e.rt.hasImage(first) {
		t.Fatal("first image tag still exists after replacement")
	}
	if !e.rt.hasImage(second) {
		t.Fatal("second image tag was not registered")
	}
	member, err = e.db.GetMember(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("GetMember after second save: %v", err)
	}
	if member.Image != second {
		t.Fatalf("member image after second save = %q, want %q", member.Image, second)
	}
	for _, purpose := range []EnvironmentPurpose{EnvironmentPurposeRun, EnvironmentPurposeTerminal} {
		plan, planErr := e.sched.BuildEnvironmentPlan(t.Context(), nil, e.ws, member, profileForTerminalTest(), purpose)
		if planErr != nil {
			t.Fatalf("BuildEnvironmentPlan(%q): %v", purpose, planErr)
		}
		if plan.Image != second {
			t.Fatalf("purpose %q image = %q, want %q", purpose, plan.Image, second)
		}
	}
}

func TestResetEnvironmentStopsTerminalAndClearsImage(t *testing.T) {
	e := newTestEnv(t, nil)
	terminal, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	saved, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("SaveEnvironment: %v", err)
	}
	if err = e.sched.ResetEnvironment(t.Context(), e.member.ID); err != nil {
		t.Fatalf("ResetEnvironment: %v", err)
	}
	if _, rowErr := e.db.GetTerminal(t.Context(), e.member.ID); !errors.Is(rowErr, store.ErrNotFound) {
		t.Fatalf("terminal row after reset = %v, want ErrNotFound", rowErr)
	}
	if _, getErr := e.rt.get(runtime.ID(terminal.ContainerID)); !errors.Is(getErr, runtime.ErrNotFound) {
		t.Fatalf("terminal container after reset = %v, want ErrNotFound", getErr)
	}
	member, err := e.db.GetMember(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("GetMember after reset: %v", err)
	}
	if member.Image != "" {
		t.Fatalf("member image after reset = %q, want empty", member.Image)
	}
	if e.rt.hasImage(saved) {
		t.Fatalf("saved image %q still exists after reset", saved)
	}
}

func TestResetEnvironmentWithoutTerminalOrImageIsNoop(t *testing.T) {
	e := newTestEnv(t, nil)
	if err := e.sched.ResetEnvironment(context.Background(), e.member.ID); err != nil {
		t.Fatalf("ResetEnvironment: %v", err)
	}
	member, err := e.db.GetMember(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if member.Image != "" || strings.TrimSpace(member.DisplayName) == "" {
		t.Fatalf("member after no-op reset = %+v", member)
	}
}

func TestSaveEnvironmentRetriesTagsStillInUse(t *testing.T) {
	e := newTestEnv(t, nil)
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	first, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("first SaveEnvironment: %v", err)
	}
	// A run container still holds the first tag when the second save
	// happens: the daemon refuses the removal, the save still succeeds.
	e.rt.holdImage(first, true)
	time.Sleep(1100 * time.Millisecond)
	second, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("second SaveEnvironment: %v", err)
	}
	if !e.rt.hasImage(first) || !e.rt.hasImage(second) {
		t.Fatalf("images after refused removal: first=%v second=%v, want both kept", e.rt.hasImage(first), e.rt.hasImage(second))
	}
	// Once the container is gone, the next reset sweeps every stale tag.
	e.rt.holdImage(first, false)
	if err := e.sched.ResetEnvironment(t.Context(), e.member.ID); err != nil {
		t.Fatalf("ResetEnvironment: %v", err)
	}
	if e.rt.hasImage(first) || e.rt.hasImage(second) {
		t.Fatalf("images after reset: first=%v second=%v, want none", e.rt.hasImage(first), e.rt.hasImage(second))
	}
}
