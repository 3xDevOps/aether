package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/overlay"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "sync",
		short: "live-overlay a local directory onto a run's worktree",
		run:   runSync,
	})
}

func runSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	live := fs.Bool("live", false, "bidirectional live overlay (required; the only mode)")
	force := fs.Bool("force", false, "overlay even while the run is running (the agent may be mid-write)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*live {
		return fmt.Errorf("usage: aether sync --live [--force] <local-dir> <run>")
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: aether sync --live [--force] <local-dir> <run>")
	}
	localDir, runID := fs.Arg(0), fs.Arg(1)

	cfg, err := cli.Load()
	if err != nil {
		return err
	}
	conn, err := cli.Dial(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	// Foreground until a termination signal, terminal run status, or
	// connection drop.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopSignals := cancelOnSignal(cancel)
	defer stopSignals()

	// Run-status watch: a terminal transition ends the overlay. A dead
	// event stream (server or connection gone) ends it too - mutagen
	// would otherwise retry the endpoint forever.
	watchDone, err := watchRunStatus(ctx, conn, runID, cancel)
	if err != nil {
		return err
	}

	sess, err := overlay.NewSession(overlay.Options{
		LocalDir: localDir,
		Dial: func(context.Context) (io.ReadWriteCloser, error) {
			return conn.Sync(runID, *force)
		},
	})
	if err != nil {
		return err
	}
	// Clean mutagen teardown on every exit path: Ctrl-C, terminal
	// status, channel drop, conflict pause, and errors all funnel here.
	defer sess.Close()

	if err := sess.Start(ctx, runID); err != nil {
		return err
	}
	fmt.Printf("syncing %s <-> run %s (Ctrl-C to stop)\n", sess.LocalDir(), runID)

	runErr := sess.Run(ctx)
	cancel()
	<-watchDone

	var conflict *overlay.Conflict
	if errors.As(runErr, &conflict) {
		// Both members are notified through the server; the overlay is
		// already paused and the local losing sides preserved.
		if perr := publishSyncConflict(conn, runID, conflict); perr != nil {
			fmt.Fprintf(os.Stderr, "aether: conflict notification failed: %v\n", perr)
		}
	}
	return runErr
}

// terminationSignals unwind the sync teardown. SIGTERM belongs here with
// Ctrl-C: without it a `kill`, a systemd stop, or a container stop kills
// the process outright, skipping every defer, so the mutagen endpoint
// never shuts down gracefully and the aether-overlay-* data directory is
// left on disk.
var terminationSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// cancelOnSignal cancels the sync context on the first termination
// signal so the normal defers run. The returned function stops the
// handler and releases its goroutine.
func cancelOnSignal(cancel context.CancelFunc) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, terminationSignals...)
	go func() {
		if _, ok := <-ch; !ok {
			return
		}
		fmt.Fprintln(os.Stderr, "aether: stopping sync")
		cancel()
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			// Stop first: it guarantees no further sends on ch, so
			// closing it to free the goroutine cannot panic.
			signal.Stop(ch)
			close(ch)
		})
	}
}

// watchRunStatus subscribes to the run's status events and cancels the
// overlay when the run reaches a terminal state or the stream dies. The
// returned channel closes when the watcher goroutine exits.
func watchRunStatus(ctx context.Context, conn *cli.Conn, runID string, cancel context.CancelFunc) (<-chan struct{}, error) {
	stream, err := conn.Events(protocol.SubscribeRequest{
		RunID: runID,
		Types: []string{string(events.TypeRunStatus)},
	})
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		defer func() { _ = stream.Close() }()
		go func() {
			<-ctx.Done()
			_ = stream.Close() // unblock the read loop
		}()
		r := bufio.NewReaderSize(stream, 64<<10)
		for {
			line, rerr := protocol.ReadLine(r)
			if rerr != nil {
				if ctx.Err() == nil {
					fmt.Fprintln(os.Stderr, "aether: event stream lost; stopping sync")
				}
				return
			}
			var ev protocol.Event
			if json.Unmarshal(line, &ev) != nil {
				continue
			}
			var payload events.RunStatusPayload
			if json.Unmarshal(ev.Payload, &payload) != nil {
				continue
			}
			if payload.To.Terminal() {
				fmt.Fprintf(os.Stderr, "aether: run is %s; stopping sync\n", payload.To)
				return
			}
		}
	}()
	return done, nil
}

// publishSyncConflict reports the paused overlay to the server so both
// affected members see the sync.conflict event.
func publishSyncConflict(conn *cli.Conn, runID string, c *overlay.Conflict) error {
	client, err := conn.Control()
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	return client.Call(protocol.MethodSyncConflict, protocol.SyncConflictParams{
		RunID:         runID,
		SyncSessionID: c.SessionID,
		Files:         c.Files,
	}, nil)
}
