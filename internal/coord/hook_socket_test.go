package coord

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type shortHookWriteConn struct{ net.Conn }

func (c shortHookWriteConn) Write(p []byte) (int, error) {
	if len(p) > 7 {
		p = p[:7]
	}
	return c.Conn.Write(p)
}

func TestHookResponseCompletesFullLineAfterPartialWrites(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close() //nolint:errcheck // test cleanup
	defer client.Close() //nolint:errcheck // test cleanup
	done := make(chan error, 1)
	go func() {
		done <- writeHookResponse(shortHookWriteConn{server}, protocol.Response{
			ID: json.RawMessage(`1`), Result: json.RawMessage(`{"wait_supported":true,"wake_admitted":true}`),
		})
	}()
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(client).ReadBytes('\n')
	if err != nil {
		t.Fatalf("incomplete frame: %q, %v", line, err)
	}
	var response protocol.Response
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	var status protocol.CoordStatusResult
	if err := json.Unmarshal(response.Result, &status); err != nil {
		t.Fatal(err)
	}
	if !status.WakeAdmitted || !status.WaitSupported {
		t.Fatalf("partial writes corrupted acceptance: %s", line)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHookResponseBoundsStalledPeer(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close() //nolint:errcheck // test cleanup
	defer client.Close() //nolint:errcheck // test cleanup
	done := make(chan error, 1)
	go func() {
		done <- writeHookResponse(server, protocol.Response{ID: json.RawMessage(`1`), Result: json.RawMessage(`{}`)})
	}()
	select {
	case err := <-done:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("stalled peer write = %v, want timeout", err)
		}
	case <-time.After(hookWriteTimeout + time.Second):
		t.Fatal("stalled observer pinned frame admission beyond write deadline")
	}
}

type finalByteHookConn struct {
	net.Conn
	ready  chan struct{}
	resume chan struct{}
}

func (c finalByteHookConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p[:len(p)-1])
	if err != nil {
		return n, err
	}
	close(c.ready)
	<-c.resume
	last, err := c.Conn.Write(p[n:])
	return n + last, err
}

func TestHookWaitFullFramePrecedesLateConsumerWithoutBlockingOtherRuns(t *testing.T) {
	h := newHarness(t, 2, func(c *Config) { c.WakeAdmission = allowHookWake })
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "shared.go")
	for _, pair := range [][2]domain.RunID{{a, b}, {b, a}} {
		if _, err := h.svc.Send(context.Background(), pair[0], sendParams(pair[1], "frame ordering")); err != nil {
			t.Fatal(err)
		}
	}
	server, client := net.Pipe()
	defer server.Close() //nolint:errcheck // test cleanup
	defer client.Close() //nolint:errcheck // test cleanup
	ready, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	defer resume()
	written := make(chan error, 1)
	go func() {
		written <- h.svc.serveHookWait(context.Background(), finalByteHookConn{server, ready, release}, b,
			protocol.Request{ID: json.RawMessage(`1`), Method: protocol.MethodCoordHookStatus, Params: json.RawMessage(`{}`)},
			protocol.Response{ID: json.RawMessage(`1`)})
	}()
	hookDone := make(chan hookReply, 1)
	go func() {
		var reply hookReply
		line, err := bufio.NewReader(client).ReadBytes('\n')
		reply.err = err
		if err == nil {
			var response protocol.Response
			reply.err = json.Unmarshal(line, &response)
			if reply.err == nil {
				reply.err = json.Unmarshal(response.Result, &reply.status)
			}
		}
		hookDone <- reply
	}()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("wake did not reach the final frame byte")
	}
	// The remaining newline is part of acceptance, not an optional flush.
	// Probe the registration boundary without relying on goroutine timing.
	admission := h.svc.inboxAdmissionLock(b)
	if admission.TryLock() {
		admission.Unlock()
		t.Fatal("consumer registration can pass an incomplete wake frame")
	}
	type inboxReply struct {
		result protocol.CoordInboxResult
		err    *protocol.Error
	}
	consume := func(run domain.RunID) <-chan inboxReply {
		done := make(chan inboxReply, 1)
		go func() {
			result, err := h.svc.Inbox(context.Background(), run, protocol.CoordInboxParams{WaitSeconds: 30})
			done <- inboxReply{result, err}
		}()
		return done
	}
	late := consume(b)
	select {
	case reply := <-consume(a):
		if reply.err != nil || len(reply.result.Messages) != 1 || reply.result.AckToken == "" {
			t.Fatalf("other run consumer = %+v", reply)
		}
	case <-time.After(time.Second):
		t.Fatal("stalled wake frame held the global service boundary")
	}
	select {
	case reply := <-late:
		t.Fatalf("late consumer passed an incomplete frame: %+v", reply)
	default:
	}
	resume()
	if reply := awaitHookReply(t, hookDone); !reply.status.WakeAdmitted {
		t.Fatalf("frame-first response was suppressed: %+v", reply.status)
	}
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wake write did not finish")
	}
	select {
	case reply := <-late:
		if reply.err != nil || len(reply.result.Messages) != 1 || reply.result.AckToken == "" {
			t.Fatalf("late consumer lost durable mail: %+v", reply)
		}
	case <-time.After(time.Second):
		t.Fatal("late consumer did not follow the completed frame")
	}
}
