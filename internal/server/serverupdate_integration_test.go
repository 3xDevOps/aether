//go:build integration

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/serverupdate"
)

const (
	updatedServerBinary = "aether-server from the release"
	updatedCLIBinary    = "aether from the release"
)

// stubRelease serves the release assets for tag the way GitHub does, so
// the whole update path - resolve, download, verify, swap - runs for real
// against something the test controls.
func stubRelease(t *testing.T, tag string) *httptest.Server {
	t.Helper()
	suffix := "-" + runtime.GOOS + "-" + runtime.GOARCH
	assets := map[string]string{
		"aether-server" + suffix: updatedServerBinary,
		"aether" + suffix:        updatedCLIBinary,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/tag/"+tag, http.StatusFound)
	})
	base := "/releases/download/" + tag
	mux.HandleFunc(base+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		for name, body := range assets {
			sum := sha256.Sum256([]byte(body))
			_, _ = fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), name)
		}
	})
	for name, body := range assets {
		mux.HandleFunc(base+"/"+name, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestIntegrationServerUpdateAppliesWhenIdle drives the scheduled path end
// to end through a wired server: an admin schedules an update while a real
// containerized run is working, the poll loop leaves it alone, and the
// moment the run parks the update applies, publishes its phases, replaces
// both binaries, and re-executes.
//
// The re-exec itself is the one thing that cannot run here - it would
// replace the test process - so Config.SelfUpdate supplies an exec that
// records the call and then blocks, exactly as syscall.Exec never returns.
func TestIntegrationServerUpdateAppliesWhenIdle(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	rt, image, verifyNoLeaks := pickRuntime(t)
	dataDir := filepath.Join(t.TempDir(), "data")

	// The binaries the update replaces. They are inert files, not the test
	// process: the service is told where they are.
	binDir := t.TempDir()
	serverBin := filepath.Join(binDir, "aether-server")
	cliBin := filepath.Join(binDir, "aether")
	for _, path := range []string{serverBin, cliBin} {
		if err := os.WriteFile(path, []byte("the installed binary"), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	release := stubRelease(t, "v9.9.9")
	execed := make(chan []string, 1)
	// The exec stub holds the poll loop the way syscall.Exec does - by not
	// returning - until the assertions have read the swapped binaries. The
	// test process is never replaced, so it releases and lets the stub
	// report the one thing a real exec only reports on failure.
	hold := make(chan struct{})
	var releaseOnce sync.Once
	releaseExec := func() { releaseOnce.Do(func() { close(hold) }) }
	t.Cleanup(releaseExec)

	srv, err := New(ctx, Config{
		DataDir:      dataDir,
		Addr:         "127.0.0.1:0",
		Runtime:      rt,
		PollInterval: 200 * time.Millisecond,
		SelfUpdate: serverupdate.Config{
			Checker:    selfupdate.NewChecker(release.URL, time.Hour),
			Executable: serverBin,
			Exec: func(_ string, argv, _ []string) error {
				execed <- argv
				<-hold
				return errors.New("the test process is never replaced")
			},
		},
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	keyPath, signer := writeClientKey(t)
	member := &domain.Member{
		DisplayName: "Update Admin",
		PublicKey:   string(ssh.MarshalAuthorizedKey(signer.PublicKey())),
		Color:       "#e6194b",
		Role:        domain.RoleAdmin,
	}
	ws := &domain.Workspace{
		Name:        "update",
		Environment: domain.WorkspaceEnvironment{CustomImage: image},
		BaseBranch:  domain.DefaultBaseBranch,
	}
	if err = srv.Store().CreateMember(ctx, member); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if err = srv.Store().CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	sub, err := srv.Bus().Subscribe(ctx, events.SubscribeOptions{Buffer: 4096})
	if err != nil {
		t.Fatalf("subscribe bus: %v", err)
	}
	defer func() { _ = sub.Close() }()
	var seen []events.Event

	runDone := make(chan error, 1)
	runCtx, stopServer := context.WithCancel(ctx)
	defer stopServer()
	go func() { runDone <- srv.Run(runCtx) }()
	addr := waitSSHAddr(t, srv)

	seedDir := t.TempDir()
	repoURL := fmt.Sprintf("ssh://aether@%s/%s.git", addr, ws.ID)
	gitEnv := append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -i "+keyPath+
			" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes")
	runGit(t, seedDir, gitEnv, "init", "-q", "-b", "main")
	runGit(t, seedDir, gitEnv, "config", "user.name", "E2E")
	runGit(t, seedDir, gitEnv, "config", "user.email", "e2e@localhost")
	runGit(t, seedDir, gitEnv, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(seedDir, "agent.sh"), agentScript)
	runGit(t, seedDir, gitEnv, "add", "-A")
	runGit(t, seedDir, gitEnv, "commit", "-q", "-m", "seed")
	runGit(t, seedDir, gitEnv, "push", "-q", repoURL, "main")

	t.Setenv("AETHER_FAKE_AGENT", "sh /workspace/agent.sh")
	client := dialSSH(t, addr, signer)
	ctrl := openControl(t, client)
	var launched protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(ws.ID), Task: "hold the server busy", Harness: "fake",
	}, &launched); err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	runID := domain.RunID(launched.Run.ID)
	att := openAttach(t, client, launched.Run.ID)
	att.waitOutput(t, "agent-ready")

	// The status method answers before anything is scheduled.
	var status protocol.ServerUpdateStatusResult
	if err := ctrl.Call(protocol.MethodServerUpdateStatus, struct{}{}, &status); err != nil {
		t.Fatalf("server.update_status: %v", err)
	}
	if !status.Capable || status.Pending != nil {
		t.Fatalf("status before scheduling = %+v, want capable with nothing pending", status)
	}

	var scheduled protocol.ServerUpdateResult
	if err := ctrl.Call(protocol.MethodServerUpdate, protocol.ServerUpdateParams{
		Version: "v9.9.9", When: protocol.ServerUpdateIdle,
	}, &scheduled); err != nil {
		t.Fatalf("server.update: %v", err)
	}
	if scheduled.Status != protocol.ServerUpdateScheduled || scheduled.Version != "v9.9.9" {
		t.Fatalf("schedule result = %+v, want a scheduled v9.9.9", scheduled)
	}
	phaseOf := func(want events.ServerUpdatePhase) func(events.Event) bool {
		return func(e events.Event) bool {
			p, ok := e.Payload.(events.ServerUpdatePayload)
			return ok && p.Phase == want
		}
	}
	ev := waitEvent(t, sub, &seen, "server.update scheduled", phaseOf(events.ServerUpdateScheduled))
	if ev.ActorID != member.ID {
		t.Fatalf("scheduled event actor = %q, want %q", ev.ActorID, member.ID)
	}

	// Several poll intervals pass with the run working; the binary must
	// still be the installed one.
	time.Sleep(2 * time.Second)
	if body, rerr := os.ReadFile(serverBin); rerr != nil || string(body) != "the installed binary" {
		t.Fatalf("aether-server = %q (%v) while a run was working, want it untouched", body, rerr)
	}
	if err := ctrl.Call(protocol.MethodServerUpdateStatus, struct{}{}, &status); err != nil {
		t.Fatalf("server.update_status: %v", err)
	}
	if status.Pending == nil || status.Pending.Version != "v9.9.9" || status.Pending.RequestedBy != string(member.ID) {
		t.Fatalf("pending = %+v, want v9.9.9 by the admin", status.Pending)
	}

	// Finish the run. The agent exits on the injected line and the run
	// parks, which is the first idle tick.
	if err := ctrl.Call(protocol.MethodRunInject, protocol.RunInjectParams{
		RunID: launched.Run.ID, Message: "done",
	}, nil); err != nil {
		t.Fatalf("run.inject: %v", err)
	}
	att.waitEnd(t)
	waitEvent(t, sub, &seen, "run.status needs-attention", func(e events.Event) bool {
		p, ok := e.Payload.(events.RunStatusPayload)
		return ok && e.RunID == runID && p.To == domain.RunNeedsAttention
	})

	waitEvent(t, sub, &seen, "server.update applying", phaseOf(events.ServerUpdateApplying))
	waitEvent(t, sub, &seen, "server.update restarting", phaseOf(events.ServerUpdateRestarting))

	select {
	case argv := <-execed:
		if len(argv) == 0 || argv[0] != serverBin {
			t.Fatalf("re-exec argv = %v, want argv[0] = %q", argv, serverBin)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the idle update never re-executed the new binary")
	}
	if body, rerr := os.ReadFile(serverBin); rerr != nil || string(body) != updatedServerBinary {
		t.Fatalf("aether-server = %q (%v), want the release build", body, rerr)
	}
	if body, rerr := os.ReadFile(cliBin); rerr != nil || string(body) != updatedCLIBinary {
		t.Fatalf("aether = %q (%v), want the release build", body, rerr)
	}

	releaseExec()
	stopServer()
	if err := <-runDone; err != nil {
		t.Fatalf("server.Run: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("server.Close: %v", err)
	}
	verifyNoLeaks(t)
}
