package protocol_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/overlap"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// TestCoordWireV2ErrorsGolden pins testdata/coord-v2/errors.ndjson to the
// bytes the real coordination service produces: each documented failure
// is provoked against internal/coord and the raw response read off the
// run's unix socket, so an error string or code changed in the service
// breaks this golden rather than only the service's own unit tests.
// requests.ndjson and success.ndjson stay pinned by TestCoordWireV2Golden,
// which marshals the same structs both endpoints serialize.
func TestCoordWireV2ErrorsGolden(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("coord.Provision binds an AF_UNIX listener and chmods the run directory to 0700 and the socket to 0666")
	}
	ctx := context.Background()
	runs := &wireRuns{runs: make(map[domain.RunID]*domain.Run)}
	runs.set(domain.Run{ID: "run_01", WorkspaceID: "ws_01", MemberID: "mem_01", Status: domain.RunRunning})
	runs.set(domain.Run{ID: "run_02", WorkspaceID: "ws_01", MemberID: "mem_02", Status: domain.RunRunning})
	runs.set(domain.Run{ID: "run_09", WorkspaceID: "ws_01", MemberID: "mem_03", Status: domain.RunRunning})

	cfg := coord.Config{
		Dir:   filepath.Join(t.TempDir(), "coord"),
		Store: runs,
		Mail:  fullMail{},
		Bus:   nopBus{},
		Peers: fixedPeers{entries: []overlap.Entry{{
			RunID: "run_01",
			With:  []overlap.Peer{{RunID: "run_02", MemberID: "mem_02", Files: []string{"src/auth.go"}}},
		}}},
	}
	svc, err := coord.New(cfg)
	if err != nil {
		t.Fatalf("coord.New: %v", err)
	}
	defer svc.Close() //nolint:errcheck // test teardown
	dir, err := svc.Provision(ctx, "run_01", nil)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	conn, err := net.Dial("unix", filepath.Join(dir, coord.SocketName))
	if err != nil {
		t.Fatalf("dial coordination socket: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown
	r := bufio.NewReader(conn)
	call := func(id int, method string, params any) []byte {
		t.Helper()
		req := protocol.Request{JSONRPC: "2.0", ID: json.RawMessage(strconv.Itoa(id)), Method: method}
		if params != nil {
			raw, merr := json.Marshal(params)
			if merr != nil {
				t.Fatalf("marshal params: %v", merr)
			}
			req.Params = raw
		}
		line, merr := json.Marshal(req)
		if merr != nil {
			t.Fatalf("marshal request: %v", merr)
		}
		if _, werr := conn.Write(append(line, '\n')); werr != nil {
			t.Fatalf("write request %d: %v", id, werr)
		}
		resp, rerr := r.ReadBytes('\n')
		if rerr != nil {
			t.Fatalf("read response %d: %v", id, rerr)
		}
		return bytes.TrimSuffix(resp, []byte("\n"))
	}
	send := func(to, body string) protocol.CoordSendParams {
		return protocol.CoordSendParams{ToRunID: to, Body: body}
	}

	got := make([][]byte, 0, 8)
	// 1: the target is alive but shares no overlap edge with the sender.
	got = append(got, call(1, protocol.MethodCoordSend, send("run_09", "ping")))
	// 2: the same target, no longer a run the store knows.
	runs.del("run_09")
	got = append(got, call(2, protocol.MethodCoordSend, send("run_09", "ping")))
	// 3: an authorized peer that has finished.
	runs.set(domain.Run{ID: "run_02", WorkspaceID: "ws_01", MemberID: "mem_02", Status: domain.RunFailed})
	got = append(got, call(3, protocol.MethodCoordSend, send("run_02", "ping")))
	// 4: the peer is back, but its inbox is at the depth cap.
	runs.set(domain.Run{ID: "run_02", WorkspaceID: "ws_01", MemberID: "mem_02", Status: domain.RunRunning})
	got = append(got, call(4, protocol.MethodCoordSend, send("run_02", "ping")))
	// Each send above spent a burst token; spend the last one so the next
	// send is throttled.
	call(99, protocol.MethodCoordSend, send("run_09", "ping"))
	// 5: over the send rate.
	got = append(got, call(5, protocol.MethodCoordSend, send("run_02", "ping")))
	// 6: an oversized body, refused before the rate limit is consulted.
	got = append(got, call(6, protocol.MethodCoordSend,
		send("run_02", strings.Repeat("a", protocol.CoordMaxBodyBytes+1))))
	// 7: the kill switch. A disabled service binds no socket, so this is
	// the one line provoked through the exported method: the error is the
	// service's, only the envelope is assembled here.
	disabled, err := coord.New(coord.Config{
		Dir: filepath.Join(t.TempDir(), "off"), Store: runs, Mail: fullMail{},
		Bus: nopBus{}, Peers: fixedPeers{}, Disabled: true,
	})
	if err != nil {
		t.Fatalf("coord.New disabled: %v", err)
	}
	_, rpcErr := disabled.Send(ctx, "run_01", send("run_02", "ping"))
	if rpcErr == nil {
		t.Fatal("disabled Send succeeded, want an error")
	}
	line7, err := json.Marshal(protocol.Response{JSONRPC: "2.0", ID: json.RawMessage("7"), Error: rpcErr})
	if err != nil {
		t.Fatalf("marshal disabled response: %v", err)
	}
	got = append(got, line7)
	// 8: a control verb; the socket's method set is closed.
	got = append(got, call(8, "run.kill", nil))

	want := readGoldenLines(t, "errors.ndjson")
	if len(want) != len(got) {
		t.Fatalf("errors.ndjson has %d lines, produced %d", len(want), len(got))
	}
	for i := range got {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("errors.ndjson line %d:\n got %s\nwant %s", i+1, got[i], want[i])
		}
	}
}

func readGoldenLines(t *testing.T, name string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "coord-v2", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	var out [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			out = append(out, line)
		}
	}
	return out
}

// wireRuns is a mutable coord.Runs: each scenario reshapes the run table
// between requests.
type wireRuns struct {
	mu   sync.Mutex
	runs map[domain.RunID]*domain.Run
}

func (f *wireRuns) GetRun(_ context.Context, id domain.RunID) (*domain.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *r
	return &cp, nil
}

func (f *wireRuns) GetMember(context.Context, domain.MemberID) (*domain.Member, error) {
	return nil, store.ErrNotFound
}

func (f *wireRuns) ListActiveRuns(context.Context) ([]*domain.Run, error) { return nil, nil }

func (f *wireRuns) set(r domain.Run) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs[r.ID] = &r
}

func (f *wireRuns) del(id domain.RunID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.runs, id)
}

// fullMail is a mailbox at its depth cap. The embedded interface keeps it
// satisfying store.MessageStore; anything unimplemented panics if reached.
type fullMail struct{ store.MessageStore }

func (fullMail) AppendRunMessage(context.Context, *store.RunMessage, int) error {
	return store.ErrInboxFull
}

func (fullMail) CountUnackedRunMessages(context.Context, domain.RunID) (int, error) { return 0, nil }

func (fullMail) DeliverRunMessages(context.Context, domain.RunID, string, int) ([]*store.RunMessage, string, error) {
	return nil, "", nil
}

type nopBus struct{}

func (nopBus) Publish(_ context.Context, e events.Event) (events.Event, error) { return e, nil }

func (nopBus) Subscribe(context.Context, events.SubscribeOptions) (events.Subscription, error) {
	panic("nopBus: Subscribe is not used by this test")
}

func (nopBus) Close() error { return nil }

type fixedPeers struct{ entries []overlap.Entry }

func (p fixedPeers) Overlaps(context.Context) ([]overlap.Entry, error) { return p.entries, nil }
