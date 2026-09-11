package main

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"
)

// Teardown trigger: a termination signal must cancel the sync context so
// the deferred mutagen shutdown and temp-state cleanup actually run. Go
// only delivers SIGTERM on unix; on Windows the signal is not
// deliverable, so this half skips.
func TestCancelOnSignalCancelsOnTermination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM is not deliverable to a process on windows")
	}
	for _, sig := range terminationSignals {
		t.Run(sig.String(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stop := cancelOnSignal(cancel)
			defer stop()

			proc, err := os.FindProcess(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			if err := proc.Signal(sig); err != nil {
				t.Fatalf("send %v: %v", sig, err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				t.Fatalf("context not canceled after %v; teardown would be skipped", sig)
			}
		})
	}
}

// The stop function is deferred on every exit path and must tolerate
// being called more than once without panicking on a closed channel.
func TestCancelOnSignalStopIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := cancelOnSignal(cancel)
	stop()
	stop()
	select {
	case <-ctx.Done():
		t.Fatal("stopping the handler canceled the context")
	default:
	}
}
