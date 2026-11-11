//go:build integration

package server

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
)

// A tailnet identity that first connects after the upgrade, with no edge
// configured, lands pending and, once approved, reaches no workspace,
// session, template or note until it is granted one, and then that one.
func TestIntegrationWorkspaceAccessTailnetNewcomer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dataDir := filepath.Join(shortTempDir(t), "data")
	_, key := writeClientKey(t)
	workspaces := []string{"ws-one", "ws-two"}
	seedPreAccess(t, dataDir, []*preAccessMember{{id: "m-ada", role: "admin", key: key}}, workspaces)

	whois := &stubWhoIs{}
	whois.set(sshd.WhoIsIdentity{}, errors.New("not a tailnet address"))
	srv, err := New(ctx, Config{DataDir: dataDir, Addr: "127.0.0.1:0", Runtime: newE2ERuntime(), StandardImage: "e2e/fake", WhoIs: whois})
	if err != nil {
		t.Fatalf("server.New on the v%d database: %v", preAccessSchemaVersion, err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- srv.Run(runCtx) }()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("server run: %v", err)
		}
	})
	addr := waitSSHAddr(t, srv)
	ada := openControl(t, dialSSH(t, addr, key))

	whois.set(sshd.WhoIsIdentity{Login: "tia@example.com", NodeID: "node-tia"}, nil)
	client, err := dialNoAuth(addr, "tia", nil)
	if err != nil {
		t.Fatalf("the newcomer's tailnet dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	tia := openControl(t, client)
	me := memberInfo(t, tia)
	if !me.Pending {
		t.Fatalf("the tailnet newcomer is %+v, want pending", me)
	}
	if err := ada.Call(protocol.MethodMemberApprove, protocol.MemberApproveParams{MemberID: me.ID}, nil); err != nil {
		t.Fatalf("member.approve: %v", err)
	}

	if got := reachOf(t, tia, workspaces); !sameReach(got, reach{Workspaces: map[string]string{}}) {
		t.Errorf("the approved tailnet newcomer reaches %+v, want nothing", got)
	}
	for _, ws := range workspaces {
		missing := missingID(ws)
		assertLikeMissing(t, "newcomer workspace.get", call(tia, protocol.MethodWorkspaceGet, protocol.WorkspaceGetParams{WorkspaceID: ws}),
			call(tia, protocol.MethodWorkspaceGet, protocol.WorkspaceGetParams{WorkspaceID: missing}), ws, missing)
		run, missingRun := "run-"+ws, missingID("run-"+ws)
		assertLikeMissing(t, "newcomer run.get", call(tia, protocol.MethodRunGet, protocol.RunIDParams{RunID: run}),
			call(tia, protocol.MethodRunGet, protocol.RunIDParams{RunID: missingRun}), run, missingRun)
	}

	if err := ada.Call(protocol.MethodWorkspaceAccessGrant, protocol.WorkspaceAccessGrantParams{WorkspaceID: "ws-two", MemberIDs: []string{me.ID}}, nil); err != nil {
		t.Fatalf("workspace.access.grant ws-two: %v", err)
	}
	want := reach{Workspaces: map[string]string{"ws-two": "collaborator"}, Runs: []string{"run-ws-two"}, Templates: []string{"nightly-ws-two"}, Notes: []string{"note-ws-two"}}
	if got := reachOf(t, tia, workspaces); !sameReach(got, want) {
		t.Errorf("the newcomer granted ws-two reaches %+v, want %+v", got, want)
	}
}
