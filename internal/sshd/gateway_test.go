package sshd

import (
	"context"
	"errors"
	"testing"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// fakePatcher answers RunPatch from a canned patch, or an error naming a
// server path - which must never reach the client.
type fakePatcher struct {
	patch gitengine.Patch
	err   error
}

func (f *fakePatcher) RunPatch(context.Context, domain.RunID, int) (gitengine.Patch, error) {
	return f.patch, f.err
}

type fakeDisk struct {
	usage disk.Usage
	err   error
}

func (f *fakeDisk) Usage() (disk.Usage, error) { return f.usage, f.err }

func wireErrOf(t *testing.T, err error) *protocol.Error {
	t.Helper()
	var pe *protocol.Error
	if err == nil || !errors.As(err, &pe) {
		t.Fatalf("want wire error, got %v", err)
	}
	return pe
}

func TestRunPatchReturnsDiffAndTruncation(t *testing.T) {
	patcher := &fakePatcher{patch: gitengine.Patch{
		Base:      "abc123",
		Text:      "diff --git a/main.go b/main.go\n",
		Truncated: true,
	}}
	e := newTestEnv(t, func(c *Config) { c.Services.Patch = patcher })
	c := controlClient(t, e)

	var got protocol.RunPatchResult
	if err := c.Call(protocol.MethodRunPatch, protocol.RunIDParams{RunID: string(e.run.ID)}, &got); err != nil {
		t.Fatalf("run.patch: %v", err)
	}
	if got.RunID != string(e.run.ID) || got.Base != "abc123" || got.Patch != patcher.patch.Text || !got.Truncated {
		t.Errorf("run.patch = %+v, want the fake's patch with truncation", got)
	}

	// An unknown run is a NotFound from the store, not a patch failure.
	err := c.Call(protocol.MethodRunPatch, protocol.RunIDParams{RunID: "run_missing"}, nil)
	if pe := wireErrOf(t, err); pe.Code != protocol.CodeNotFound {
		t.Errorf("unknown run code = %d, want %d", pe.Code, protocol.CodeNotFound)
	}

	// A run with no checkout answers Unavailable without echoing the
	// engine's error, which names paths on the server.
	patcher.err = errors.New("stat /srv/aether/checkouts/run_1: no such file or directory")
	err = c.Call(protocol.MethodRunPatch, protocol.RunIDParams{RunID: string(e.run.ID)}, nil)
	pe := wireErrOf(t, err)
	if pe.Code != protocol.CodeUnavailable {
		t.Errorf("no-checkout code = %d, want %d", pe.Code, protocol.CodeUnavailable)
	}
	if pe.Message != "run.patch: this run has no checkout to diff" {
		t.Errorf("no-checkout message = %q, must not echo server paths", pe.Message)
	}
}

func TestRunPatchWithoutSeamIsUnavailable(t *testing.T) {
	e := newTestEnv(t, nil)
	c := controlClient(t, e)
	err := c.Call(protocol.MethodRunPatch, protocol.RunIDParams{RunID: string(e.run.ID)}, nil)
	if pe := wireErrOf(t, err); pe.Code != protocol.CodeUnavailable {
		t.Errorf("nil seam code = %d, want %d", pe.Code, protocol.CodeUnavailable)
	}
}

func TestServerDiskRoundTripsUsage(t *testing.T) {
	reader := &fakeDisk{usage: disk.Usage{
		FreeBytes:       1,
		UsedBytes:       2,
		TotalBytes:      3,
		WorktreeBytes:   4,
		TranscriptBytes: 5,
		DatabaseBytes:   6,
	}}
	e := newTestEnv(t, func(c *Config) { c.Services.Disk = reader })
	c := controlClient(t, e)

	var got protocol.ServerDiskResult
	if err := c.Call(protocol.MethodServerDisk, nil, &got); err != nil {
		t.Fatalf("server.disk: %v", err)
	}
	want := protocol.ServerDiskResult{
		UsedBytes: 2, TotalBytes: 3, FreeBytes: 1,
		WorktreeBytes: 4, TranscriptBytes: 5, DatabaseBytes: 6,
	}
	if got != want {
		t.Errorf("server.disk = %+v, want %+v", got, want)
	}

	// A filesystem read failure names the data-directory path; the client
	// gets Unavailable without it.
	reader.err = errors.New("statfs /srv/aether: permission denied")
	err := c.Call(protocol.MethodServerDisk, nil, nil)
	pe := wireErrOf(t, err)
	if pe.Code != protocol.CodeUnavailable {
		t.Errorf("read failure code = %d, want %d", pe.Code, protocol.CodeUnavailable)
	}
	if pe.Message != "server.disk: the data directory's filesystem could not be read" {
		t.Errorf("read failure message = %q, must not echo server paths", pe.Message)
	}
}

func TestServerDiskWithoutSeamIsUnavailable(t *testing.T) {
	e := newTestEnv(t, nil)
	c := controlClient(t, e)
	err := c.Call(protocol.MethodServerDisk, nil, nil)
	if pe := wireErrOf(t, err); pe.Code != protocol.CodeUnavailable {
		t.Errorf("nil seam code = %d, want %d", pe.Code, protocol.CodeUnavailable)
	}
}
