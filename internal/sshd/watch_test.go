package sshd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/watch"
)

// The watch methods over a real control channel against the real service:
// a watch is its creator's alone, admins included, and what cannot be
// watched is refused with why.
func TestWatchesBelongToTheirCreator(t *testing.T) {
	t.Parallel()
	var e *testEnv
	e = newTestEnv(t, func(c *Config) {
		svc, err := watch.New(watch.Config{
			Store: c.Store.(*store.DB), Bus: c.Bus, Runs: c.Runs,
			Swarm: func(ctx context.Context, run *domain.Run) (*protocol.AttentionItem, domain.MemberID, error) {
				return e.srv.SwarmAttention(ctx, run)
			},
		})
		if err != nil {
			t.Fatalf("watch.New: %v", err)
		}
		c.Services.Watches = svc
	})
	adminSigner, _ := addMember(t, e, "Root", domain.RoleAdmin, false)
	bobSigner, _ := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	ada, admin, bob := controlClient(t, e), controlAs(t, e, adminSigner), controlAs(t, e, bobSigner)
	run := createRunWithStatus(t, e, domain.RunRunning)

	create := protocol.WatchCreateParams{Target: protocol.WatchTarget{Kind: protocol.WatchTargetRun, RunID: string(run.ID)}, IdempotencyKey: "first"}
	var created protocol.WatchResult
	if err := ada.Call(protocol.MethodWatchCreate, create, &created); err != nil {
		t.Fatalf("watch.create: %v", err)
	}
	if w := created.Watch; w.State != protocol.WatchActive || strings.Join(w.Conditions, ",") != "needs_you,finished" || w.Link != "/?page=settings&id=watches&watch="+w.ID {
		t.Fatalf("created = %+v", w)
	}
	if !strings.HasPrefix(created.Receipt, "Watching run archive me for: needs you, finished.") || !strings.Contains(created.Receipt, "in-app only") {
		t.Fatalf("receipt = %q", created.Receipt)
	}
	var again protocol.WatchResult
	if err := ada.Call(protocol.MethodWatchCreate, create, &again); err != nil || again.Watch.ID != created.Watch.ID {
		t.Fatalf("repeated idempotency key = %s, %v; want %s", again.Watch.ID, err, created.Watch.ID)
	}
	create.IdempotencyKey = ""
	if err := ada.Call(protocol.MethodWatchCreate, create, &again); err != nil || again.Watch.ID != created.Watch.ID {
		t.Fatalf("same target and conditions = %s, %v; want the live watch %s", again.Watch.ID, err, created.Watch.ID)
	}

	for name, c := range map[string]*protocol.Client{"admin": admin, "teammate": bob} {
		var list protocol.WatchListResult
		if err := c.Call(protocol.MethodWatchList, protocol.WatchListParams{}, &list); err != nil || len(list.Watches) != 0 {
			t.Fatalf("%s watch.list = %+v, %v; want none of Ada's", name, list.Watches, err)
		}
		var pe *protocol.Error
		if err := c.Call(protocol.MethodWatchGet, protocol.WatchIDParams{WatchID: created.Watch.ID}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeNotFound {
			t.Fatalf("%s watch.get = %v, want not found", name, err)
		}
		if err := c.Call(protocol.MethodWatchDelete, protocol.WatchIDParams{WatchID: created.Watch.ID}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeNotFound {
			t.Fatalf("%s watch.delete = %v, want not found", name, err)
		}
	}

	paused := true
	var updated protocol.WatchResult
	if err := ada.Call(protocol.MethodWatchUpdate, protocol.WatchUpdateParams{WatchID: created.Watch.ID, Paused: &paused}, &updated); err != nil || updated.Watch.State != protocol.WatchPaused {
		t.Fatalf("pause = %+v, %v", updated.Watch, err)
	}

	for _, refused := range []struct {
		params protocol.WatchCreateParams
		want   string
	}{
		{protocol.WatchCreateParams{Target: create.Target, Conditions: []string{"deployed"}}, "no deploy signal"},
		{protocol.WatchCreateParams{Target: create.Target, Conditions: []string{"ci"}}, "not supported yet"},
		{protocol.WatchCreateParams{Target: protocol.WatchTarget{Kind: protocol.WatchTargetPR, Repo: "o/r", Number: 1}}, "not supported yet"},
		{protocol.WatchCreateParams{Target: create.Target, Conditions: []string{"delivered"}}, "does not apply to a run"},
		{protocol.WatchCreateParams{Target: create.Target, ExpiresAt: "2099-01-01T00:00:00Z"}, "more than 90 days"},
	} {
		var pe *protocol.Error
		if err := ada.Call(protocol.MethodWatchCreate, refused.params, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams || !strings.Contains(pe.Message, refused.want) {
			t.Fatalf("watch.create %+v = %v, want invalid params saying %q", refused.params, err, refused.want)
		}
	}

	var prefs protocol.NotifyPrefs
	if err := ada.Call(protocol.MethodNotifyPrefsGet, struct{}{}, &prefs); err != nil || prefs.Push == nil || !*prefs.Push || prefs.QuietHours != nil {
		t.Fatalf("default prefs = %+v, %v; want push on", prefs, err)
	}
	off := false
	if err := ada.Call(protocol.MethodNotifyPrefsSet, protocol.NotifyPrefs{Push: &off}, &prefs); err != nil || *prefs.Push {
		t.Fatalf("push off = %+v, %v", prefs, err)
	}
	var pe *protocol.Error
	bad := protocol.NotifyPrefs{QuietHours: &protocol.QuietHours{Start: "22:00", End: "07:00", TZ: "Mars/Olympus"}}
	if err := ada.Call(protocol.MethodNotifyPrefsSet, bad, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
		t.Fatalf("an unknown time zone = %v, want invalid params", err)
	}
	bad.QuietHours.TZ = "Europe/Berlin"
	if err := ada.Call(protocol.MethodNotifyPrefsSet, bad, &prefs); err != nil || *prefs.Push || prefs.QuietHours == nil || prefs.QuietHours.TZ != "Europe/Berlin" {
		t.Fatalf("set quiet hours alone = %+v, %v; want them set and push still off", prefs, err)
	}

	if err := ada.Call(protocol.MethodWatchDelete, protocol.WatchIDParams{WatchID: created.Watch.ID}, nil); err != nil {
		t.Fatalf("watch.delete: %v", err)
	}
}
