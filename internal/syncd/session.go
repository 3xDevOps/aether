package syncd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// runSession drives one connected session over the events subsystem
// stream: subscribe (resuming from the last seen cursor), full catch-up
// fetch and push, then follow git.branch events until the stream dies.
// The returned bool reports whether the subscription was acknowledged.
func (d *Daemon) runSession(ctx context.Context, stream io.ReadWriteCloser) (bool, error) {
	defer func() { _ = stream.Close() }()

	req := protocol.SubscribeRequest{
		Types: []string{string(events.TypeGitBranch)},
		// Resume from the last seen cursor so nothing between sessions
		// is missed; the catch-up fetch below covers the ref state, the
		// replay covers events racing that fetch.
		Replay:   d.lastSeq > 0,
		AfterSeq: d.lastSeq,
	}
	line, err := json.Marshal(req)
	if err != nil {
		return false, fmt.Errorf("marshal subscribe: %w", err)
	}
	if _, writeErr := stream.Write(append(line, '\n')); writeErr != nil {
		return false, fmt.Errorf("write subscribe: %w", writeErr)
	}
	r := bufio.NewReaderSize(stream, 64<<10)
	ackLine, err := protocol.ReadLine(r)
	if err != nil {
		return false, fmt.Errorf("read subscribe ack: %w", err)
	}
	var ack protocol.SubscribeResponse
	if err := json.Unmarshal(ackLine, &ack); err != nil {
		return false, fmt.Errorf("decode subscribe ack: %w", err)
	}
	if !ack.OK {
		// CodeUnavailable here means the cursor cannot be replayed
		// (no event log); drop it and let catch-up fetches carry the
		// sync instead of resubscribing with the same doomed cursor.
		if ack.Code == protocol.CodeUnavailable {
			d.lastSeq = 0
		}
		return false, fmt.Errorf("subscribe refused: rpc error %d: %s", ack.Code, ack.Error)
	}

	// Catch-up: the offline story. Everything that moved while
	// disconnected is picked up here; events only sharpen latency.
	d.syncAll(ctx)

	// Reader goroutine: a blocked ReadLine only unblocks when the
	// connection dies, so the select loop below owns cancellation and
	// stream.Close (via the deferred close + client teardown) unblocks it.
	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		defer close(lines)
		for {
			l, rerr := protocol.ReadLine(r)
			if rerr != nil {
				readErr <- rerr
				return
			}
			select {
			case lines <- l:
			case <-ctx.Done():
				readErr <- ctx.Err()
				return
			}
		}
	}()

	pushT := time.NewTicker(d.cfg.PushInterval)
	defer pushT.Stop()
	catchupT := time.NewTicker(d.cfg.CatchupInterval)
	defer catchupT.Stop()

	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-pushT.C:
			if err := d.push(ctx); err != nil {
				slog.Warn("syncd: push", "error", err)
			}
		case <-catchupT.C:
			d.syncAll(ctx)
		case l, ok := <-lines:
			if !ok {
				// Server closed the stream; with the sshd contract that
				// includes buffer drops, so resume-with-replay from
				// lastSeq recovers anything lost.
				err := <-readErr
				if errors.Is(err, io.EOF) {
					err = errors.New("event stream closed by server")
				}
				return true, err
			}
			var ev protocol.Event
			if uerr := json.Unmarshal(l, &ev); uerr != nil {
				return true, fmt.Errorf("decode event: %w", uerr)
			}
			if ev.Seq > d.lastSeq {
				d.lastSeq = ev.Seq
			}
			if wantsFetch(ev, d.cfg.WorkspaceID) {
				if ferr := d.fetch(ctx); ferr != nil {
					slog.Warn("syncd: fetch", "error", ferr)
				}
			}
		}
	}
}

// wantsFetch reports whether ev should trigger a run-branch fetch: a
// git.branch event whose payload names our workspace (any workspace when
// workspaceID is empty).
func wantsFetch(ev protocol.Event, workspaceID string) bool {
	if ev.Type != string(events.TypeGitBranch) {
		return false
	}
	if workspaceID == "" {
		return true
	}
	var p events.GitBranchPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return false
	}
	return string(p.WorkspaceID) == workspaceID
}

// syncAll is one full catch-up pass: fetch every run branch, push the
// local base, and advance the server's base branch to origin's tip when
// origin syncing is on. Origin runs after the push so a stale local base
// never blocks it and a member's unpublished local work always wins over
// a rewrite of the server's base.
func (d *Daemon) syncAll(ctx context.Context) {
	if err := d.fetch(ctx); err != nil {
		slog.Warn("syncd: catch-up fetch", "error", err)
	}
	if err := d.push(ctx); err != nil {
		slog.Warn("syncd: catch-up push", "error", err)
	}
	if d.cfg.SyncOrigin {
		if err := d.syncOrigin(ctx); err != nil {
			slog.Warn("syncd: origin sync", "error", err)
		}
	}
}
