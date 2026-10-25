package scheduler

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

type fakeEgress map[string]string

func (f fakeEgress) Prepare(context.Context) (map[string]string, error) { return f, nil }

func TestLaunchPutsAnEgressLimitedRunBehindTheProxy(t *testing.T) {
	t.Parallel()
	const proxy = "http://172.30.0.1:3128"
	e := newTestEnv(t, func(cfg *Config) { cfg.Egress = fakeEgress{"HTTPS_PROXY": proxy} })
	ctx := t.Context()
	environment := domain.WorkspaceEnvironment{Variables: map[string]string{"HTTPS_PROXY": "http://corp.example:8080"}}
	if err := e.db.SetWorkspaceEnvironment(ctx, e.ws.ID, environment); err != nil {
		t.Fatal(err)
	}
	if _, open := e.launchFake(t, "open"); open.spec.Egress || open.spec.Env["HTTPS_PROXY"] != "http://corp.example:8080" {
		t.Fatalf("run with egress control off: Egress %t, HTTPS_PROXY %q; want the workspace's own proxy", open.spec.Egress, open.spec.Env["HTTPS_PROXY"])
	}

	environment.Egress = domain.EgressPolicy{On: true, Hosts: []string{"github.com"}}
	if err := e.db.SetWorkspaceEnvironment(ctx, e.ws.ID, environment); err != nil {
		t.Fatal(err)
	}
	if _, limited := e.launchFake(t, "limited"); !limited.spec.Egress || limited.spec.Env["HTTPS_PROXY"] != proxy {
		t.Fatalf("run with egress control on: Egress %t, HTTPS_PROXY %q; want the egress network and %s", limited.spec.Egress, limited.spec.Env["HTTPS_PROXY"], proxy)
	}

	bare := newTestEnv(t, nil)
	if err := bare.db.SetWorkspaceEnvironment(ctx, bare.ws.ID, environment); err != nil {
		t.Fatal(err)
	}
	_, err := bare.sched.Launch(ctx, bare.ws.ID, bare.member.ID, bare.member.ID, "no proxy", "fake", domain.LaunchTUI)
	if err == nil || !strings.Contains(err.Error(), "workspace egress control needs the Docker runtime") {
		t.Fatalf("Launch without an egress proxy = %v, want the Docker runtime refusal", err)
	}
}

// Turning egress control off applies to new runs only: a limited run that
// moves to another account across a server restart stays limited.
func TestAccountMoveKeepsTheRunsEgressControl(t *testing.T) {
	t.Parallel()
	const proxy = "http://172.30.0.1:3128"
	e, _, notes := newFallbackEnv(t, func(cfg *Config) {
		delete(cfg.Harnesses, "claude")
		cfg.Egress = fakeEgress{"HTTPS_PROXY": proxy}
	})
	e.share(t)
	ctx := t.Context()
	if err := e.db.SetWorkspaceEnvironment(ctx, e.ws.ID, domain.WorkspaceEnvironment{Egress: domain.EgressPolicy{On: true}}); err != nil {
		t.Fatal(err)
	}
	startScheduler(t, e.sched)
	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "fix it", "claude", domain.LaunchHeadless)
	if err != nil {
		t.Fatal(err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	<-e.sched.RecoveryReady()
	if err = e.db.SetWorkspaceEnvironment(ctx, e.ws.ID, domain.WorkspaceEnvironment{}); err != nil {
		t.Fatal(err)
	}
	if _, err = e.bus.Publish(ctx, events.Event{WorkspaceID: e.ws.ID, RunID: run.ID, Payload: events.RunUsageLimitPayload{
		Message: "You've hit your session limit · resets 3pm (UTC)", SessionID: "sess-1",
	}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the usage limit recorded", func() bool {
		sc, scErr := e.sched.readSidecar(run.ID)
		return scErr == nil && sc.UsageLimit != ""
	})
	if err = e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	e.rt.byName(string(run.ID)).exitNow(1)
	cfg := e.cfg
	cfg.PTY = newFakePTY()
	cfg.PTY.(*fakePTY).logDir = e.pty.logDir
	sched, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sched.Close() })
	sched.UseUsageFallback(UsageFallback{Usage: claudeUsage{e.owner.ID: 10}, AuthorizationMu: &sync.Mutex{}, Note: notes.post})
	startScheduler(t, sched)

	if spec := e.assertMovedToOwner(t, run.ID, notes); !spec.Egress || spec.Env["HTTPS_PROXY"] != proxy {
		t.Fatalf("replacement container: Egress %t, HTTPS_PROXY %q; want the run's egress control kept", spec.Egress, spec.Env["HTTPS_PROXY"])
	}
}
