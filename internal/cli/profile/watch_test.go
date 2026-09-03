package profile

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	profilesvc "github.com/3xDevOps/Aether/internal/profile"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestNoProfileSyncProducesNoAutomaticUploads(t *testing.T) {
	var n atomic.Int32
	root := t.TempDir()
	w := &Watcher{
		Disabled: true,
		Roots:    map[string]string{"claude": root},
		PushOne: func(context.Context, *protocol.Client, string) error {
			n.Add(1)
			return nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil && err != context.DeadlineExceeded && err != context.Canceled {
		t.Fatalf("Run: %v", err)
	}
	if err := w.CatchUp(ctx, nil); err != nil {
		t.Fatalf("CatchUp: %v", err)
	}
	if n.Load() != 0 || w.PushCount() != 0 || w.CatchUpCount() != 0 {
		t.Fatalf("disabled watcher uploaded n=%d pushes=%d catchup=%d", n.Load(), w.PushCount(), w.CatchUpCount())
	}
}

func TestManualPushPathIndependentOfWatcher(t *testing.T) {
	root := setupClaudeRoot(t)
	mustWrite(t, root+"/settings.json", `{"ok":true}`)
	files, err := Discover(t.Context(), "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	params, err := BuildPushParams(nil, "claude", files, nil, "ws_1")
	if err != nil {
		t.Fatal(err)
	}
	if params.Harness != "claude" || len(params.Paths) != 1 || len(params.Blobs) != 1 {
		t.Fatalf("manual push params = %+v", params)
	}
	if params.WorkspaceID != "ws_1" {
		t.Errorf("workspace_id = %q", params.WorkspaceID)
	}
}
func TestDefaultPushOneUploadsEmptySnapshot(t *testing.T) {
	setupClaudeRoot(t)
	server, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = clientConn.Close()
	})
	pushed := make(chan protocol.ProfilePushParams, 1)
	go func() {
		reader := bufio.NewReader(server)
		for {
			line, err := protocol.ReadLine(reader)
			if err != nil {
				return
			}
			var req protocol.Request
			if json.Unmarshal(line, &req) != nil {
				return
			}
			var result any
			switch req.Method {
			case protocol.MethodProfileStatus:
				result = protocol.ProfileStatusResult{}
			case protocol.MethodProfilePush:
				var params protocol.ProfilePushParams
				if json.Unmarshal(req.Params, &params) != nil {
					return
				}
				pushed <- params
				result = protocol.ProfilePushResult{Snapshot: protocol.ProfileSnapshot{ID: "empty"}}
			default:
				return
			}
			raw, _ := json.Marshal(result)
			resp, _ := json.Marshal(protocol.Response{JSONRPC: "2.0", ID: req.ID, Result: raw})
			if _, err := server.Write(append(resp, '\n')); err != nil {
				return
			}
		}
	}()

	client := protocol.NewClient(clientConn)
	if err := defaultPushOne(t.Context(), client, "claude"); err != nil {
		t.Fatalf("defaultPushOne: %v", err)
	}
	select {
	case params := <-pushed:
		if len(params.Paths) != 0 || len(params.Blobs) != 0 {
			t.Fatalf("empty snapshot params = %+v", params)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("empty profile root was not pushed")
	}
}

func TestCatchUpOncePerReconnect(t *testing.T) {
	var n atomic.Int32
	root := t.TempDir()
	dials := make(chan *fakeConn, 2)
	w := &Watcher{
		Debounce: 10 * time.Millisecond,
		Roots:    map[string]string{"claude": root},
		Dial: func(context.Context) (Conn, error) {
			select {
			case c := <-dials:
				return c, nil
			default:
				return nil, io.EOF
			}
		},
		PushOne: func(context.Context, *protocol.Client, string) error {
			n.Add(1)
			return nil
		},
	}
	c1 := newFakeConn()
	c2 := newFakeConn()
	dials <- c1
	dials <- c2
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	waitCatchups(t, w, 1)
	c1.drop()
	waitCatchups(t, w, 2)
	cancel()
	<-done
	if n.Load() < 2 {
		t.Fatalf("catch-up pushes = %d, want at least 2 (one per session)", n.Load())
	}
}

type fakeConn struct {
	done chan struct{}
}

func newFakeConn() *fakeConn                 { return &fakeConn{done: make(chan struct{})} }
func (c *fakeConn) Client() *protocol.Client { return nil }
func (c *fakeConn) Done() <-chan struct{}    { return c.done }
func (c *fakeConn) Close() error {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	return nil
}
func (c *fakeConn) drop() { _ = c.Close() }

func waitCatchups(t *testing.T, w *Watcher, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w.CatchUpCount() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("catch-up count = %d, want %d", w.CatchUpCount(), want)
}

// TestDefaultPushOneLogsSkippedFiles covers the daemon's share of the
// size caps. It pushes unattended, so a file left behind would otherwise
// be absent from the server with nobody told - where before the caps
// existed the push failed loudly instead.
func TestDefaultPushOneLogsSkippedFiles(t *testing.T) {
	root := setupClaudeRoot(t)
	mustWrite(t, filepath.Join(root, "settings.json"), `{"ok":true}`)
	mustWrite(t, filepath.Join(root, "notes", "huge.txt"), strings.Repeat("x", profilesvc.MaxFileBytes+1))

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	server, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = clientConn.Close()
	})
	go func() {
		reader := bufio.NewReader(server)
		for {
			line, err := protocol.ReadLine(reader)
			if err != nil {
				return
			}
			var req protocol.Request
			if json.Unmarshal(line, &req) != nil {
				return
			}
			var result any
			switch req.Method {
			case protocol.MethodProfileStatus:
				result = protocol.ProfileStatusResult{}
			case protocol.MethodProfilePush:
				result = protocol.ProfilePushResult{Snapshot: protocol.ProfileSnapshot{ID: "psn_1"}}
			default:
				return
			}
			raw, _ := json.Marshal(result)
			resp, _ := json.Marshal(protocol.Response{JSONRPC: "2.0", ID: req.ID, Result: raw})
			if _, err := server.Write(append(resp, '\n')); err != nil {
				return
			}
		}
	}()

	if err := defaultPushOne(t.Context(), protocol.NewClient(clientConn), "claude"); err != nil {
		t.Fatalf("defaultPushOne: %v", err)
	}
	out := logged.String()
	if !strings.Contains(out, "notes/huge.txt") || !strings.Contains(out, ExcludeTooLarge) {
		t.Fatalf("the daemon pushed a partial profile without saying so:\n%s", out)
	}
}
