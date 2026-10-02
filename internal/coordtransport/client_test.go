package coordtransport

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func transportSocket(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "act-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "coord.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(40 * time.Second))
		serve(conn)
	}()
	return path
}

// This is a real deadline regression: a legitimate server result arriving just
// past the old 30-second client ceiling must still be consumed. Subtests share
// no state and run concurrently rather than serializing long waits.
func TestCallConsumesMaximumWaitResult(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		params       any
	}{
		{"terminal wait", protocol.MethodDevTerminalWait, protocol.DevTerminalWaitParams{TimeoutMS: 30000}},
		{"browser bootstrap", protocol.MethodDevBrowserOpen, protocol.DevBrowserOpenParams{URL: "about:blank"}},
		{"browser reset", protocol.MethodDevBrowserReset, protocol.DevBrowserResetParams{}},
		{"browser wait including companion margin", protocol.MethodDevBrowserWait, protocol.DevBrowserWaitParams{TimeoutMS: 30000}},
		{"mission long poll", protocol.MethodMissionPlanShow, protocol.MissionPlanShowParams{WaitSeconds: 30}},
		{"inbox long poll", protocol.MethodCoordInbox, protocol.CoordInboxParams{WaitSeconds: 30}},
		{"mission pointer params", protocol.MethodMissionPlanShow, &protocol.MissionPlanShowParams{WaitSeconds: 30}},
		{"native hook long poll", protocol.MethodCoordHookStatus, protocol.CoordHookStatusParams{WaitSeconds: 30}},
		{"native hook pointer params", protocol.MethodCoordHookStatus, &protocol.CoordHookStatusParams{WaitSeconds: 30}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stop := make(chan struct{})
			t.Cleanup(func() { close(stop) })
			path := transportSocket(t, func(conn net.Conn) {
				if _, err := protocol.ReadLine(bufio.NewReader(conn)); err != nil {
					return
				}
				delay := 30*time.Second + 100*time.Millisecond
				if tc.method == protocol.MethodDevBrowserWait {
					delay += 5 * time.Second
				}
				timer := time.NewTimer(delay)
				defer timer.Stop()
				select {
				case <-timer.C:
					_, _ = io.WriteString(conn, "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"timed_out\":true}}\n")
				case <-stop:
				}
			})
			var result struct {
				TimedOut bool `json:"timed_out"`
			}
			ctx, cancel := context.WithTimeout(context.Background(), 39*time.Second)
			defer cancel()
			if err := Call(ctx, path, tc.method, tc.params, &result); err != nil || !result.TimedOut {
				t.Fatalf("valid maximum-wait response lost: result=%+v error=%v", result, err)
			}
		})
	}
}

func TestCallBoundsInvalidPollWait(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		params       any
		bound        time.Duration
	}{
		{"mission excessive wait", protocol.MethodMissionPlanShow, protocol.MissionPlanShowParams{WaitSeconds: 300}, 35 * time.Second},
		{"native hook excessive wait", protocol.MethodCoordHookStatus, protocol.CoordHookStatusParams{WaitSeconds: 300}, 35 * time.Second},
		{"native hook negative wait", protocol.MethodCoordHookStatus, protocol.CoordHookStatusParams{WaitSeconds: -300}, 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			accepted := make(chan struct{})
			disconnected := make(chan struct{})
			path := transportSocket(t, func(conn net.Conn) {
				if _, err := protocol.ReadLine(bufio.NewReader(conn)); err != nil {
					return
				}
				close(accepted)
				_, _ = conn.Read(make([]byte, 1))
				close(disconnected)
			})
			ctx, cancel := context.WithTimeout(context.Background(), tc.bound)
			defer cancel()
			err := Call(ctx, path, tc.method, tc.params, nil)
			if ErrorCode(err) != protocol.CodeUnavailable || ctx.Err() != nil {
				t.Fatalf("client did not bound invalid wait: error=%v context=%v", err, ctx.Err())
			}
			select {
			case <-accepted:
			default:
				t.Fatal("invalid wait prevented request from reaching the server")
			}
			select {
			case <-disconnected:
			case <-time.After(2 * time.Second):
				t.Fatal("deadline retained socket")
			}
		})
	}
}

func TestCallCancellationClosesInFlightDevelopmentRequest(t *testing.T) {
	for _, method := range []string{protocol.MethodDevBrowserOpen, protocol.MethodDevTerminalWait} {
		t.Run(method, func(t *testing.T) {
			accepted := make(chan struct{})
			disconnected := make(chan struct{})
			path := transportSocket(t, func(conn net.Conn) {
				if _, err := protocol.ReadLine(bufio.NewReader(conn)); err != nil {
					return
				}
				close(accepted)
				_, _ = conn.Read(make([]byte, 1))
				close(disconnected)
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan error, 1)
			go func() { finished <- Call(ctx, path, method, nil, nil) }()
			select {
			case <-accepted:
			case <-time.After(2 * time.Second):
				t.Fatal("request did not reach server")
			}
			cancel()
			select {
			case err := <-finished:
				if ErrorCode(err) != protocol.CodeUnavailable {
					t.Fatalf("cancellation lost transport status: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancel did not interrupt blocked response")
			}
			select {
			case <-disconnected:
			case <-time.After(2 * time.Second):
				t.Fatal("cancel retained socket")
			}
		})
	}
}

func TestDevelopmentControlFrameLimits(t *testing.T) {
	oversized := json.RawMessage(`{"text":"` + strings.Repeat("a", 64<<10) + `"}`)
	err := Call(context.Background(), filepath.Join(t.TempDir(), "missing.sock"), protocol.MethodDevTerminalInput, oversized, nil)
	if ErrorCode(err) != protocol.CodeInvalidParams {
		t.Fatalf("oversized request was dialed: %v", err)
	}
	for _, size := range []int{64 << 10, (64 << 10) + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			prefix := `{"jsonrpc":"2.0","id":1,"result":{"text":"`
			suffix := "\"}}\n"
			path := transportSocket(t, func(conn net.Conn) {
				if _, err := protocol.ReadLine(bufio.NewReader(conn)); err != nil {
					return
				}
				_, _ = io.WriteString(conn, prefix+strings.Repeat("a", size-len(prefix)-len(suffix))+suffix)
			})
			var result struct {
				Text string `json:"text"`
			}
			err := Call(context.Background(), path, protocol.MethodDevTerminalOutput, nil, &result)
			if size == 64<<10 {
				if err != nil || len(result.Text) != size-len(prefix)-len(suffix) {
					t.Fatalf("exact-boundary response lost: %v", err)
				}
			} else if ErrorCode(err) != protocol.CodeUnavailable {
				t.Fatalf("oversized response accepted: %v", err)
			}
		})
	}
}
