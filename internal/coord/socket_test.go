package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func (h *coordHarness) dial(t *testing.T, run domain.RunID) *protocol.Client {
	t.Helper()
	conn, err := net.Dial("unix", filepath.Join(h.dir, "coord", string(run), coordtransport.SocketName))
	if err != nil {
		t.Fatalf("dial %s: %v", run, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return protocol.NewClient(conn)
}

func mode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Mode().Perm()
}

// TestCoordinationSocketRoundTrip drives the real socket the way the
// in-container bridge does, and pins the host-side modes that let an agent
// that is not root reach it through the bind mount: the coordination root
// stays private to the server, while the per-run directory and the socket
// inside it - the only things a container sees - are traversable and
// writable by any uid.
func TestCoordinationSocketRoundTrip(t *testing.T) {
	h := newHarness(t, 2)
	ctx := context.Background()
	h.start()
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "src/auth.go")

	timeline, err := h.bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeTimeline}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer timeline.Close() //nolint:errcheck // test cleanup

	dirA, err := h.svc.Provision(ctx, a, map[string][]byte{"fixture.json": []byte(`{"mcpServers":{}}`)})
	if err != nil {
		t.Fatalf("Provision(a): %v", err)
	}
	if _, perr := h.svc.Provision(ctx, b, nil); perr != nil {
		t.Fatalf("Provision(b): %v", perr)
	}
	if got := mode(t, filepath.Join(h.dir, "coord")); got != rootMode {
		t.Errorf("coordination root mode = %o, want %o", got, rootMode)
	}
	if got := mode(t, dirA); got != runDirMode {
		t.Errorf("run directory mode = %o, want %o", got, runDirMode)
	}
	if got := mode(t, filepath.Join(dirA, "fixture.json")); got != configMode {
		t.Errorf("config mode = %o, want %o", got, configMode)
	}
	if got := mode(t, filepath.Join(dirA, coordtransport.SocketName)); got != socketMode {
		t.Errorf("socket mode = %o, want %o", got, socketMode)
	}

	clientA := h.dial(t, a)
	var status protocol.CoordStatusResult
	if cerr := clientA.Call(protocol.MethodCoordStatus, nil, &status); cerr != nil {
		t.Fatalf("coord.status: %v", cerr)
	}
	if status.RunID != string(a) || len(status.Peers) != 1 || status.Peers[0].RunID != string(b) {
		t.Fatalf("status = %+v, want run %s with peer %s", status, a, b)
	}

	var sent protocol.CoordSendResult
	if serr := clientA.Call(protocol.MethodCoordSend,
		sendParams(b, "rewriting login(); ~10 min"), &sent); serr != nil {
		t.Fatalf("coord.send: %v", serr)
	}
	if sent.MessageID == "" {
		t.Fatal("coord.send returned no message id")
	}

	clientB := h.dial(t, b)
	var inbox protocol.CoordInboxResult
	if ierr := clientB.Call(protocol.MethodCoordInbox, nil, &inbox); ierr != nil {
		t.Fatalf("coord.inbox: %v", ierr)
	}
	if len(inbox.Messages) != 1 || inbox.Messages[0].ID != sent.MessageID || inbox.AckToken == "" {
		t.Fatalf("inbox = %+v, want the sent message and a token", inbox)
	}

	// Nothing outside the three methods is reachable from a container.
	err = clientA.Call(protocol.MethodRunKill, protocol.RunIDParams{RunID: string(b)}, nil)
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeMethodNotFound {
		t.Fatalf("run.kill over the coordination socket = %v, want method not found", err)
	}

	select {
	case e := <-timeline.Events():
		p, ok := e.Payload.(events.TimelinePayload)
		if !ok || e.RunID != a || e.ActorID != "" {
			t.Fatalf("timeline event = %+v, want the server-originated send on run %s", e, a)
		}
		if p.Kind != events.TimelineNote {
			t.Fatalf("timeline kind = %q, want a note", p.Kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the coordination message was never stamped into the timeline")
	}

	// Releasing a run retires its mailbox with the rest of its state.
	if err := h.svc.Release(b); err != nil {
		t.Fatalf("Release(b): %v", err)
	}
	if n, err := h.db.CountUnackedRunMessages(ctx, b); err != nil || n != 0 {
		t.Fatalf("unacked after release = %d (err %v), want 0", n, err)
	}
}
func TestMissionMethodWhitelistIntegrationSurface(t *testing.T) {
	for _, method := range []string{
		protocol.MethodIntegrationPrepare,
		protocol.MethodIntegrationShow,
		protocol.MethodIntegrationVerify,
		protocol.MethodIntegrationRequestDelivery,
		protocol.MethodIntegrationDeliver,
	} {
		if !isMissionMethod(method) {
			t.Errorf("integration method %q is not admitted to mission dispatch", method)
		}
	}
	for _, method := range []string{
		protocol.MethodIntegrationList,
		protocol.MethodIntegrationResolve,
		protocol.MethodIntegrationPatch,
		protocol.MethodIntegrationDecide,
		protocol.MethodIntegrationDelete,
	} {
		if isMissionMethod(method) {
			t.Errorf("forbidden integration method %q is admitted to mission dispatch", method)
		}
	}
}

// TestLostResponseRedelivers is the at-least-once guarantee over the real
// socket: the bridge dies between the read and the agent seeing it, so
// nothing acknowledges the batch and the retry gets it again.
// TestProvisionRefusesAssetNamesThatAreNotFiles: the asset names come from
// the harness registry, but Provision joins each one onto the run's
// directory and hands the result to a container runtime. A name that is not
// a plain file in that directory is refused, and refused before anything is
// written or removed - the run directory itself is one of the paths a bad
// name resolves to.
func TestProvisionRefusesAssetNamesThatAreNotFiles(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"/", ".", "..", "", "sub/asset.json", "../escape.json"} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, 1)
			h.start()
			run := h.run(0)
			if _, err := h.svc.Provision(ctx, run, map[string][]byte{name: []byte("x")}); err == nil {
				t.Fatalf("Provision accepted the asset name %q", name)
			}
			dir := filepath.Join(h.dir, "coord", string(run))
			info, err := os.Lstat(dir)
			if err != nil || !info.IsDir() {
				t.Fatalf("run directory after the refusal: %v (dir=%v)", err, info != nil && info.IsDir())
			}
		})
	}
}

func TestLostResponseRedelivers(t *testing.T) {
	h := newHarness(t, 2)
	ctx := context.Background()
	h.start()
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "src/auth.go")
	if _, err := h.svc.Provision(ctx, b, nil); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if _, err := h.svc.Send(ctx, a, sendParams(b, "going ahead")); err != nil {
		t.Fatalf("Send: %v", err)
	}

	first := h.dial(t, b)
	var got protocol.CoordInboxResult
	if err := first.Call(protocol.MethodCoordInbox, nil, &got); err != nil {
		t.Fatalf("coord.inbox: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close bridge: %v", err)
	}

	retry := h.dial(t, b)
	var again protocol.CoordInboxResult
	if err := retry.Call(protocol.MethodCoordInbox, nil, &again); err != nil {
		t.Fatalf("coord.inbox (retry): %v", err)
	}
	if len(again.Messages) != 1 || again.Messages[0].ID != got.Messages[0].ID || again.AckToken != got.AckToken {
		t.Fatalf("retry = %+v, want the same batch under the same token", again)
	}
	if err := retry.Call(protocol.MethodCoordInbox, protocol.CoordInboxParams{AckToken: again.AckToken}, &again); err != nil {
		t.Fatalf("coord.inbox (ack): %v", err)
	}
	if len(again.Messages) != 0 {
		t.Fatalf("after the ack = %+v, want an empty inbox", again)
	}
}

// TestRestartRecovery covers both sides of the kill switch: with
// coordination on, a surviving run's socket is rebound on a new inode and
// a finished run's directory is collected; with it off, the old socket is
// unlinked and nothing takes its place, so what is still mounted inside a
// running container goes inert.
func TestRestartRecovery(t *testing.T) {
	ctx := context.Background()

	t.Run("enabled", func(t *testing.T) {
		h := newHarness(t, 2)
		h.start()
		alive, finished := h.run(0), h.run(1)
		for _, r := range []domain.RunID{alive, finished} {
			if _, err := h.svc.Provision(ctx, r, nil); err != nil {
				t.Fatalf("Provision(%s): %v", r, err)
			}
		}
		if err := h.db.UpdateRunStatus(ctx, finished, domain.RunAbandoned, "", nil, nil); err != nil {
			t.Fatalf("finish run: %v", err)
		}
		socket := filepath.Join(h.dir, "coord", string(alive), coordtransport.SocketName)
		if cerr := h.svc.Close(); cerr != nil {
			t.Fatalf("Close: %v", cerr)
		}
		if _, serr := os.Lstat(socket); serr != nil {
			t.Fatalf("the socket must survive shutdown as the provisioning record: %v", serr)
		}
		// Scuff the mode so only a fresh bind can restore it below. Inode
		// identity cannot prove the rebind: ext4 hands the freed inode
		// number straight back to the next bind on the same path.
		if err := os.Chmod(socket, 0o600); err != nil {
			t.Fatalf("chmod socket: %v", err)
		}

		h.restart(t, false)
		if got := mode(t, socket); got != socketMode {
			t.Errorf("socket mode after recovery = %o, want %o (a reused socket would keep the scuffed mode)", got, socketMode)
		}
		if _, err := os.Lstat(filepath.Join(h.dir, "coord", string(finished))); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the finished run's directory survived recovery: %v", err)
		}
		var status protocol.CoordStatusResult
		if err := h.dial(t, alive).Call(protocol.MethodCoordStatus, nil, &status); err != nil {
			t.Fatalf("coord.status after recovery: %v", err)
		}
		if status.RunID != string(alive) {
			t.Fatalf("status after recovery = %+v, want run %s", status, alive)
		}
	})

	t.Run("retained terminal", func(t *testing.T) {
		h := newHarness(t, 3)
		h.start()
		retained, expired, sender := h.run(0), h.run(1), h.run(2)
		for _, r := range []domain.RunID{retained, expired, sender} {
			if _, err := h.svc.Provision(ctx, r, nil); err != nil {
				t.Fatalf("Provision(%s): %v", r, err)
			}
		}
		h.peers.pair(sender, retained, "src/auth.go")
		if _, err := h.svc.Send(ctx, sender, sendParams(retained, "keep this mailbox")); err != nil {
			t.Fatalf("Send(retained): %v", err)
		}
		h.advance(sendRefill)
		h.peers.pair(sender, expired, "src/auth.go")
		if _, err := h.svc.Send(ctx, sender, sendParams(expired, "retire this mailbox")); err != nil {
			t.Fatalf("Send(expired): %v", err)
		}
		for _, r := range []domain.RunID{retained, expired} {
			if err := h.db.UpdateRunStatus(ctx, r, domain.RunMerged, "", nil, nil); err != nil {
				t.Fatalf("finish run %s: %v", r, err)
			}
		}
		socket := filepath.Join(h.dir, "coord", string(retained), coordtransport.SocketName)
		if err := h.svc.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if err := os.Chmod(socket, 0o600); err != nil {
			t.Fatalf("chmod retained socket: %v", err)
		}

		h.restart(t, false, func(cfg *Config) {
			cfg.RetainsContainer = func(_ context.Context, run domain.RunID) bool {
				return run == retained
			}
		})
		if got := mode(t, socket); got != socketMode {
			t.Errorf("retained socket mode after recovery = %o, want %o", got, socketMode)
		}
		if _, err := os.Lstat(filepath.Join(h.dir, "coord", string(retained))); err != nil {
			t.Fatalf("retained run directory was removed: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(h.dir, "coord", string(expired))); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("expired run directory survived recovery: %v", err)
		}
		if n, err := h.db.CountUnackedRunMessages(ctx, retained); err != nil || n != 1 {
			t.Fatalf("retained mailbox = %d (err %v), want one message", n, err)
		}
		if n, err := h.db.CountUnackedRunMessages(ctx, expired); err != nil || n != 0 {
			t.Fatalf("expired mailbox = %d (err %v), want empty", n, err)
		}
		var inbox protocol.CoordInboxResult
		if err := h.dial(t, retained).Call(protocol.MethodCoordInbox, nil, &inbox); err != nil {
			t.Fatalf("coord.inbox for retained run: %v", err)
		}
		if len(inbox.Messages) != 1 || inbox.Messages[0].Body != "keep this mailbox" {
			t.Fatalf("retained inbox = %+v, want preserved message", inbox)
		}
	})

	t.Run("switched off", func(t *testing.T) {
		h := newHarness(t, 1)
		h.start()
		run := h.run(0)
		if _, err := h.svc.Provision(ctx, run, map[string][]byte{"fixture.json": []byte(`{}`)}); err != nil {
			t.Fatalf("Provision: %v", err)
		}
		if err := h.svc.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		h.restart(t, true)
		socket := filepath.Join(h.dir, "coord", string(run), coordtransport.SocketName)
		if _, err := os.Lstat(socket); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("socket after an off recovery = %v, want it unlinked", err)
		}
		// The mount itself stays: the container holds it open, and its
		// read-only assets are simply inert.
		if _, err := os.Lstat(filepath.Join(h.dir, "coord", string(run), "fixture.json")); err != nil {
			t.Fatalf("the mounted config must stay in place: %v", err)
		}
	})

	// A container provisioned before the v2 cutover holds a bridge that
	// dials coord.sock and parses the v1 status shape. Serving v2 on that
	// name would answer it in a shape it cannot read, so recovery retires
	// the socket instead: the bridge fails to connect and the run reports
	// coordination unavailable, which the agent already handles.
	t.Run("retires an old version", func(t *testing.T) {
		h := newHarness(t, 1)
		h.start()
		run := h.run(0)
		if _, err := h.svc.Provision(ctx, run, nil); err != nil {
			t.Fatalf("Provision: %v", err)
		}
		legacy := filepath.Join(h.dir, "coord", string(run), legacySocketName)
		if err := os.WriteFile(legacy, nil, socketMode); err != nil {
			t.Fatalf("stage a v1 socket: %v", err)
		}
		if err := h.svc.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		h.restart(t, false)
		if _, err := os.Lstat(legacy); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the v1 socket survived recovery: %v", err)
		}
		// The current version is still bound, so a re-provisioned bridge
		// keeps working.
		if _, err := os.Lstat(filepath.Join(h.dir, "coord", string(run), coordtransport.SocketName)); err != nil {
			t.Errorf("the current socket must stay bound: %v", err)
		}
	})
}

// restart builds a second service over the same data directory, the way a
// server restart does, and starts it.
func (h *coordHarness) restart(t *testing.T, disabled bool, opts ...func(*Config)) {
	t.Helper()
	svcCfg := Config{
		Dir:      filepath.Join(h.dir, "coord"),
		Store:    h.db,
		Mail:     h.db,
		Bus:      h.bus,
		Peers:    h.peers,
		Disabled: disabled,
		now:      h.now,
	}
	for _, opt := range opts {
		opt(&svcCfg)
	}
	svc, err := New(svcCfg)
	if err != nil {
		t.Fatalf("New (restart): %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start (restart): %v", err)
	}
	h.svc = svc
}

// TestSocketConnectionCap proves the accept loop is bounded. The agent
// behind the socket is semi-trusted, and an unbounded accept loop would
// let one run walk the whole server to its file descriptor limit.
func TestSocketConnectionCap(t *testing.T) {
	h := newHarness(t, 1)
	ctx := context.Background()
	h.start()
	run := h.run(0)
	if _, err := h.svc.Provision(ctx, run, nil); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// Each connection completes a round trip, so the server has certainly
	// accepted it and is holding its slot.
	held := make([]*protocol.Client, 0, maxConnsPerRun)
	for i := range maxConnsPerRun {
		c := h.dial(t, run)
		var status protocol.CoordStatusResult
		if err := c.Call(protocol.MethodCoordStatus, nil, &status); err != nil {
			t.Fatalf("coord.status on connection %d: %v", i, err)
		}
		held = append(held, c)
	}

	over := h.dial(t, run)
	var status protocol.CoordStatusResult
	if err := over.Call(protocol.MethodCoordStatus, nil, &status); err == nil {
		t.Fatal("the connection past the cap was served, want it closed")
	}

	// Releasing a slot lets the next connection through again.
	if err := held[0].Close(); err != nil {
		t.Fatalf("close a held connection: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		next := h.dial(t, run)
		if err := next.Call(protocol.MethodCoordStatus, nil, &status); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the freed slot was never reused")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestIdleConnectionIsDropped proves a bridge that stops speaking is
// reaped, and - just as important - that an active one is not: the
// deadline has to be reset for every request, not set once per connection.
func TestIdleConnectionIsDropped(t *testing.T) {
	const idle = 150 * time.Millisecond
	h := newHarness(t, 1, func(c *Config) { c.idle = idle })
	ctx := context.Background()
	h.start()
	run := h.run(0)
	if _, err := h.svc.Provision(ctx, run, nil); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	client := h.dial(t, run)
	var status protocol.CoordStatusResult
	for i := range 3 {
		if err := client.Call(protocol.MethodCoordStatus, nil, &status); err != nil {
			t.Fatalf("coord.status on an active connection (call %d): %v", i, err)
		}
		time.Sleep(idle / 3)
	}

	time.Sleep(2 * idle)
	if err := client.Call(protocol.MethodCoordStatus, nil, &status); err == nil {
		t.Fatal("an idle connection was still served, want it dropped")
	}
}

// TestWedgedWriterIsDropped covers the other way a connection can stall:
// a bridge that pipelines requests and never reads responses fills the
// socket buffer until the server's write blocks, out of the read
// deadline's reach. The write deadline must reap that state too.
func TestWedgedWriterIsDropped(t *testing.T) {
	const idle = 150 * time.Millisecond
	h := newHarness(t, 1, func(c *Config) { c.idle = idle })
	ctx := context.Background()
	h.start()
	run := h.run(0)
	if _, err := h.svc.Provision(ctx, run, nil); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	conn, err := net.Dial("unix", filepath.Join(h.dir, "coord", string(run), coordtransport.SocketName))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test cleanup
	req := []byte(`{"jsonrpc":"2.0","id":1,"method":"coord.status"}` + "\n")

	// Keep writing without ever reading. Once both directions' buffers are
	// full our writes time out; the server, wedged in its response write,
	// must close the connection when its own deadline fires, which turns
	// the timeouts into a hard write error.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if derr := conn.SetWriteDeadline(time.Now().Add(50 * time.Millisecond)); derr != nil {
			t.Fatalf("set write deadline: %v", derr)
		}
		_, werr := conn.Write(req)
		if werr == nil || errors.Is(werr, os.ErrDeadlineExceeded) {
			continue
		}
		return // the server dropped the wedged connection
	}

	t.Fatal("the server never dropped a connection that stopped reading responses")
}

// missionTransportStub is the mission authority: every run in mission is
// assigned to one mission and is a peer of the others; every other run is
// ordinary. HandleAgent fails with err.
type missionTransportStub struct {
	err     error
	mission []domain.RunID
}

func (m missionTransportStub) Assignment(_ context.Context, run domain.RunID) (protocol.CoordMissionAssignment, error) {
	if !slices.Contains(m.mission, run) {
		return protocol.CoordMissionAssignment{}, nil
	}
	return protocol.CoordMissionAssignment{MissionID: "mission-1", Capabilities: coordinationCapabilities}, nil
}

func (m missionTransportStub) Peers(_ context.Context, run domain.RunID) ([]protocol.CoordPeer, error) {
	peers := make([]protocol.CoordPeer, 0, len(m.mission))
	for _, id := range m.mission {
		if id != run {
			peers = append(peers, protocol.CoordPeer{RunID: string(id)})
		}
	}
	return peers, nil
}

func (m missionTransportStub) HandleAgent(context.Context, domain.RunID, string, json.RawMessage) (any, error) {
	return nil, m.err
}

func (m missionTransportStub) ValidateReport(context.Context, domain.RunID) error {
	return nil
}

func (m missionTransportStub) ReconcileReport(context.Context, domain.RunID, *store.CoordReport, protocol.EvidencePacket) error {
	return nil
}

func TestMissionTransportMapsMissionErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		cause    error
		wantCode int
	}{
		"mission": {
			cause:    fmt.Errorf("wrapped: %w", store.ErrMissionIdempotencyConflict),
			wantCode: protocol.CodeConflict,
		},
		"coordination": {
			cause:    fmt.Errorf("wrapped: %w", store.ErrIdempotencyConflict),
			wantCode: protocol.CodeConflict,
		},
		"accepted-revision": {
			cause:    fmt.Errorf("task revision: %w", store.ErrConflict),
			wantCode: protocol.CodeConflict,
		},
		"unauthorized-integrator": {
			cause:    fmt.Errorf("%w: integrator lookup: %w", permissions.ErrDenied, store.ErrNotFound),
			wantCode: protocol.CodeDenied,
		},
		"stale-integrator": {
			cause:    fmt.Errorf("assignment: %w", store.ErrMissionStale),
			wantCode: protocol.CodeDenied,
		},
		"missing-attempt": {
			cause:    fmt.Errorf("attempt lookup: %w", store.ErrNotFound),
			wantCode: protocol.CodeNotFound,
		},
		"active-retry": {
			cause:    &protocol.Error{Code: protocol.CodeInvalidState, Message: "worker.retry: attempt remains active"},
			wantCode: protocol.CodeInvalidState,
		},
		"phase": {
			cause:    fmt.Errorf("wrapped: %w", store.ErrMissionPhase),
			wantCode: protocol.CodeInvalidState,
		},
		// A revision the integrator may not accept alone is the same class of
		// answer as a phase refusal: the call is well-formed, the state says
		// no until a human decides.
		"amendment-required": {
			cause:    fmt.Errorf("wrapped: %w", store.ErrMissionAmendmentRequired),
			wantCode: protocol.CodeInvalidState,
		},
		"real-error": {
			cause:    errors.New("database unavailable"),
			wantCode: protocol.CodeInternal,
		},
	} {
		t.Run(name, func(t *testing.T) {
			cause, wantCode := tc.cause, tc.wantCode
			h := newHarness(t, 1, func(c *Config) {
				c.Mission = missionTransportStub{err: cause}
			})
			ctx := context.Background()
			h.start()
			run := h.run(0)
			if _, err := h.svc.Provision(ctx, run, nil); err != nil {
				t.Fatalf("Provision: %v", err)
			}

			err := h.dial(t, run).Call(protocol.MethodTaskPropose, protocol.TaskProposeParams{}, nil)
			var rpcErr *protocol.Error
			if !errors.As(err, &rpcErr) {
				t.Fatalf("task.propose error = %v, want protocol error", err)
			}
			if rpcErr.Code != wantCode {
				t.Fatalf("task.propose code = %d, want %d", rpcErr.Code, wantCode)
			}
			wantCause := cause.Error()
			var causeRPC *protocol.Error
			if errors.As(cause, &causeRPC) {
				wantCause = causeRPC.Message
			}
			if !strings.Contains(rpcErr.Message, wantCause) {
				t.Fatalf("mission error lost its cause: message %q, want cause %q", rpcErr.Message, wantCause)
			}
		})
	}
}

// TestMissionMethodWhitelistPlanGate: the allow-list, not the capability
// advertisement, is what makes a method reachable from a run. The two human
// decisions are control-channel-only and must stay unreachable from any agent.
func TestMissionMethodWhitelistPlanGate(t *testing.T) {
	for _, method := range []string{
		protocol.MethodMissionQuestionAsk,
		protocol.MethodMissionClarificationComplete,
		protocol.MethodMissionPlanShow,
		protocol.MethodMissionPlanSubmit,
	} {
		if !isMissionMethod(method) {
			t.Errorf("plan gate method %q is not admitted to mission dispatch", method)
		}
	}
	h := newHarness(t, 1)
	for _, method := range []string{
		protocol.MethodMissionQuestionAnswer,
		protocol.MethodMissionPlanDecide,
	} {
		if isMissionMethod(method) {
			t.Errorf("human decision method %q is admitted to mission dispatch", method)
		}
		line := []byte(`{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":{}}`)
		resp := h.svc.handle(context.Background(), h.runs[0].ID, line)
		if resp.Error == nil || resp.Error.Code != protocol.CodeMethodNotFound {
			t.Errorf("%s over the agent socket = %+v, want CodeMethodNotFound", method, resp.Error)
		}
	}
}
