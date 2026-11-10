package slack

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
)

// Slack acts as the linked member's connector, so a reply or a button
// press on a session in wsB fails for A, U and N exactly as on a session
// that does not exist, and reaches neither the session nor its agent; B,
// who owns the session, does both.
func TestSlackActionsOnAnUnreachableSessionReadAsMissing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	bus, err := events.NewInProc(ctx, nil)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	runs, rooms := &e2eRuns{pending: map[domain.RunID][]acphost.Request{}}, &e2eRooms{}
	srv, err := sshd.New(sshd.Config{Addr: "127.0.0.1:0", HostKeyPath: filepath.Join(t.TempDir(), "host_ed25519_key"),
		Store: h.db, Bus: bus, Git: noGit{}, PTY: noPTY{}, Runs: runs, Name: "build-box", Services: sshd.Services{Rooms: rooms}})
	if err != nil {
		t.Fatalf("sshd.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	svc, err := New(Config{Store: h.db, AppToken: "xapp-test", BotToken: "xoxb-test", Name: "build-box",
		API: h.slack.srv.URL + "/api/", HTTP: h.slack.srv.Client(),
		Local: func(ctx context.Context, grant sshd.ConnectorGrant, id string) (Caller, error) {
			if checkErr := srv.CheckConnectorGrant(ctx, grant); checkErr != nil {
				return nil, checkErr
			}
			return srv.ConnectorLocal(grant, id, Client, Scopes), nil
		}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	cast := map[string]*domain.Member{}
	for name, role := range map[string]domain.Role{"S": domain.RoleSuperAdmin, "A": domain.RoleAdmin, "B": domain.RoleAdmin,
		"U": domain.RoleCollaborator, "N": domain.RoleCollaborator} {
		m := &domain.Member{DisplayName: name, TailnetLogin: strings.ToLower(name) + "@example.com", Color: "#e6194b", Role: role}
		if err := h.db.CreateMember(ctx, m); err != nil {
			t.Fatalf("CreateMember %s: %v", name, err)
		}
		if err := h.db.PutSlackLink(ctx, &store.SlackLink{Member: m.ID, TeamID: "T1", UserID: "U-" + name, Channel: "D-" + name,
			CreatedAt: time.Now()}); err != nil {
			t.Fatalf("PutSlackLink %s: %v", name, err)
		}
		cast[name] = m
	}
	wsA, wsB := &domain.Workspace{Name: "a", OwnerID: cast["A"].ID}, &domain.Workspace{Name: "b", OwnerID: cast["B"].ID}
	for _, ws := range []*domain.Workspace{wsA, wsB} {
		if err := h.db.CreateWorkspace(ctx, ws); err != nil {
			t.Fatalf("CreateWorkspace: %v", err)
		}
	}
	if err := h.db.GrantWorkspace(ctx, wsA.ID, cast["U"].ID, cast["A"].ID); err != nil {
		t.Fatalf("GrantWorkspace: %v", err)
	}
	runB := &domain.Run{Title: "Tidy b", WorkspaceID: wsB.ID, MemberID: cast["B"].ID, Harness: "claude",
		Mode: domain.LaunchACP, ACP: true, Status: domain.RunRunning}
	if err := h.db.CreateRun(ctx, runB); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	runs.set(runB.ID, acphost.Request{ID: "req1", Kind: "permission", Title: "Delete b?", Options: []acphost.Option{
		{ID: "opt-allow", Name: "Allow", Kind: "allow_once"},
		{ID: "opt-reject", Name: "Reject", Kind: "reject_once"},
	}})
	value, err := json.Marshal(answer{Run: string(runB.ID), Answer: "req1", Option: "opt-reject"})
	if err != nil {
		t.Fatal(err)
	}

	act := func(who string) (reply, pressed string) {
		t.Helper()
		channel := "D-" + who
		if err := h.db.PutSlackMessage(ctx, &store.SlackMessage{RunID: runB.ID, Channel: channel, TS: "100.000001", NeedKey: "req1"}); err != nil {
			t.Fatalf("PutSlackMessage: %v", err)
		}
		var cb eventCallback
		cb.TeamID, cb.EventID = "T1", "Ev-"+who
		cb.Event.User, cb.Event.Channel, cb.Event.Text, cb.Event.ThreadTS = "U-"+who, channel, "use eu-west", "100.000001"
		var in interaction
		in.Type, in.User.ID, in.Team.ID = "block_actions", "U-"+who, "T1"
		in.Container.ChannelID, in.Container.MessageTS = channel, "100.000001"
		in.Actions = append(in.Actions, struct {
			Value string `json:"value"`
		}{string(value)})
		return svc.message(ctx, cb), svc.press(ctx, in)
	}
	missingInject := func(who string) string {
		t.Helper()
		c, err := svc.acting(ctx, "T1", "U-"+who)
		if err != nil {
			t.Fatalf("%s acting: %v", who, err)
		}
		err = call(ctx, c, protocol.MethodRunInject, protocol.RunInjectParams{RunID: "run-doesnotexist", Message: "use eu-west"}, &protocol.RunRoomPostResult{})
		if err == nil {
			t.Fatalf("%s run.inject of a missing session succeeded", who)
		}
		return strings.ReplaceAll(err.Error(), "run-doesnotexist", string(runB.ID))
	}
	for _, who := range []string{"A", "U", "N"} {
		reply, pressed := act(who)
		if want := missingInject(who); reply != want {
			t.Errorf("%s reply in wsB's session thread = %q, want %q as for a missing session", who, reply, want)
		}
		if pressed != "Already answered, or no longer pending." {
			t.Errorf("%s press on wsB's session = %q, want it to find no pending request", who, pressed)
		}
	}
	if got := rooms.posted(); len(got) != 0 {
		t.Fatalf("wsB's session got %q from members who cannot reach it", got)
	}
	if got := runs.answers(); len(got) != 0 {
		t.Fatalf("wsB's agent got answers %q from members who cannot reach it", got)
	}

	reply, pressed := act("B")
	if reply != "Sent to the session." || pressed != "Answered: Reject." {
		t.Fatalf("B reply = %q, press = %q; want sent and answered", reply, pressed)
	}
	if got := runs.answers(); len(got) != 1 || got[0] != "req1 opt-reject" {
		t.Fatalf("wsB's agent answers = %q, want B's", got)
	}
}
