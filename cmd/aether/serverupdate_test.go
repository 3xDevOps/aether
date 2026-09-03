package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestServerRequiresUpdateSubcommand(t *testing.T) {
	for _, args := range [][]string{nil, {"status"}, {"rollback"}} {
		err := runServer(args)
		if err == nil {
			t.Fatalf("runServer(%v) succeeded, want a usage error", args)
		}
		if got := err.Error(); got != "usage: aether server <update>" {
			t.Fatalf("runServer(%v) error = %q, want the exact usage line", args, got)
		}
	}
}

func TestParseServerUpdateDefaults(t *testing.T) {
	opts, err := parseServerUpdate(nil)
	if err != nil {
		t.Fatalf("parse with no flags: %v", err)
	}
	want := serverUpdateOptions{when: protocol.ServerUpdateNow}
	if opts != want {
		t.Fatalf("defaults = %+v, want %+v", opts, want)
	}
}

func TestParseServerUpdateVersionAndWhenIdle(t *testing.T) {
	opts, err := parseServerUpdate([]string{"--version", "v0.2.0", "--when", "idle"})
	if err != nil {
		t.Fatalf("parse version+idle: %v", err)
	}
	want := serverUpdateOptions{version: "v0.2.0", when: protocol.ServerUpdateIdle}
	if opts != want {
		t.Fatalf("options = %+v, want %+v", opts, want)
	}
}

func TestParseServerUpdateRejectsBadWhen(t *testing.T) {
	if _, err := parseServerUpdate([]string{"--when", "soon"}); err == nil {
		t.Fatal("bad --when value accepted")
	}
}

func TestParseServerUpdateRejectsStrayPositional(t *testing.T) {
	if _, err := parseServerUpdate([]string{"now"}); err == nil {
		t.Fatal("stray positional argument accepted")
	}
}

func TestParseServerUpdateCancel(t *testing.T) {
	opts, err := parseServerUpdate([]string{"--cancel"})
	if err != nil {
		t.Fatalf("parse --cancel: %v", err)
	}
	if opts.when != protocol.ServerUpdateCancel {
		t.Fatalf("when = %q, want cancel", opts.when)
	}
}

func TestParseServerUpdateCancelConflictsWithWhen(t *testing.T) {
	_, err := parseServerUpdate([]string{"--cancel", "--when", "idle"})
	if err == nil || !strings.Contains(err.Error(), "--when") {
		t.Fatalf("error = %v, want it to name --when", err)
	}
}

func TestParseServerUpdateCancelConflictsWithVersion(t *testing.T) {
	_, err := parseServerUpdate([]string{"--cancel", "--version", "v0.2.0"})
	if err == nil || !strings.Contains(err.Error(), "--version") {
		t.Fatalf("error = %v, want it to name --version", err)
	}
}

func TestParseServerUpdateStatus(t *testing.T) {
	opts, err := parseServerUpdate([]string{"--status"})
	if err != nil {
		t.Fatalf("parse --status: %v", err)
	}
	if !opts.status {
		t.Fatal("status not set")
	}
}

func TestParseServerUpdateStatusConflictsWithOtherFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--status", "--version", "v0.2.0"},
		{"--status", "--when", "idle"},
		{"--status", "--cancel"},
		{"--status", "--yes"},
	} {
		if _, err := parseServerUpdate(args); err == nil {
			t.Fatalf("parseServerUpdate(%v) succeeded, want a conflict error", args)
		}
	}
}

func TestConfirmServerUpdateNowAcceptsY(t *testing.T) {
	var out bytes.Buffer
	ok, err := confirmServerUpdateNow(strings.NewReader("y\n"), &out, "v0.2.0", 2)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !ok {
		t.Fatal("\"y\" was not accepted")
	}
	got := out.String()
	for _, want := range []string{"v0.2.0", "2 runs are active", "reattaches", "drop and reconnect"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt %q is missing %q", got, want)
		}
	}
}

func TestConfirmServerUpdateNowAcceptsYesCaseInsensitive(t *testing.T) {
	ok, err := confirmServerUpdateNow(strings.NewReader("YES\n"), &bytes.Buffer{}, "v0.2.0", 0)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !ok {
		t.Fatal("\"YES\" was not accepted")
	}
}

func TestConfirmServerUpdateNowRejectsAnythingElse(t *testing.T) {
	for _, input := range []string{"n\n", "\n", "sure\n"} {
		ok, err := confirmServerUpdateNow(strings.NewReader(input), &bytes.Buffer{}, "v0.2.0", 1)
		if err != nil {
			t.Fatalf("confirm(%q): %v", input, err)
		}
		if ok {
			t.Fatalf("input %q was accepted", input)
		}
	}
}

func TestConfirmServerUpdateNowUnresolvedVersion(t *testing.T) {
	var out bytes.Buffer
	if _, err := confirmServerUpdateNow(strings.NewReader("n\n"), &out, "", 0); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !strings.Contains(out.String(), "the latest release") {
		t.Fatalf("prompt %q does not name the latest release when no tag was given", out.String())
	}
}

// serverUpdateEventLine encodes one server.update event as the NDJSON the
// event subsystem emits, the same shape env_test.go's helper builds.
func serverUpdateEventLine(t *testing.T, payload events.ServerUpdatePayload) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	line, err := json.Marshal(protocol.Event{Type: string(events.TypeServerUpdate), Payload: raw})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return append(line, '\n')
}

func TestFollowServerUpdateDedupesRepeatedPhases(t *testing.T) {
	var stream bytes.Buffer
	// The event is published once per workspace with no workspace filter,
	// so the same phase legitimately arrives more than once.
	stream.Write(serverUpdateEventLine(t, events.ServerUpdatePayload{Phase: events.ServerUpdateApplying, Version: "v0.2.0"}))
	stream.Write(serverUpdateEventLine(t, events.ServerUpdatePayload{Phase: events.ServerUpdateApplying, Version: "v0.2.0"}))
	stream.Write(serverUpdateEventLine(t, events.ServerUpdatePayload{Phase: events.ServerUpdateRestarting, Version: "v0.2.0"}))
	stream.Write(serverUpdateEventLine(t, events.ServerUpdatePayload{Phase: events.ServerUpdateRestarting, Version: "v0.2.0"}))
	var out bytes.Buffer
	err := followServerUpdate(&stream, &out, "v0.2.0")
	if err != nil {
		t.Fatalf("follow: %v", err)
	}
	got := out.String()
	if n := strings.Count(got, "applying v0.2.0"); n != 1 {
		t.Fatalf("applying printed %d times, want 1: %q", n, got)
	}
	// "restarting on v0.2.0\n" is the deduped phase line; the stream-end
	// message also mentions restarting but with different punctuation, so
	// matching the newline keeps the two counts apart.
	if n := strings.Count(got, "restarting on v0.2.0\n"); n != 1 {
		t.Fatalf("restarting printed %d times, want 1: %q", n, got)
	}
}

func TestFollowServerUpdateFailedReturnsDetail(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(serverUpdateEventLine(t, events.ServerUpdatePayload{Phase: events.ServerUpdateApplying, Version: "v0.2.0"}))
	stream.Write(serverUpdateEventLine(t, events.ServerUpdatePayload{Phase: events.ServerUpdateFailed, Version: "v0.2.0", Detail: "checksum mismatch for aether-server"}))
	err := followServerUpdate(&stream, &bytes.Buffer{}, "v0.2.0")
	if err == nil {
		t.Fatal("failed phase reported success")
	}
	if !strings.Contains(err.Error(), "checksum mismatch for aether-server") {
		t.Fatalf("error %q is missing the detail verbatim", err.Error())
	}
}

func TestFollowServerUpdateStreamEndReportsRestarting(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(serverUpdateEventLine(t, events.ServerUpdatePayload{Phase: events.ServerUpdateApplying, Version: "v0.2.0"}))
	// No restarting event: the connection just drops, which is how a real
	// restart ends the stream.
	var out bytes.Buffer
	err := followServerUpdate(&stream, &out, "v0.2.0")
	if err != nil {
		t.Fatalf("stream end returned an error: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "connection closed") || !strings.Contains(got, "restarting on v0.2.0") || !strings.Contains(got, "reconnect") {
		t.Fatalf("stream-end message = %q, want it to say the server is restarting and to reconnect", got)
	}
}

func TestWaitingLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *protocol.ServerUpdateWaiting
		want string
	}{
		{
			name: "nothing holding it back",
			in:   nil,
			want: "nothing is holding it back; it applies on the next poll",
		},
		{
			name: "one run",
			in:   &protocol.ServerUpdateWaiting{Runs: 1},
			want: "waiting: 1 run is still working",
		},
		{
			name: "runs and shells",
			in:   &protocol.ServerUpdateWaiting{Runs: 2, Shells: 3},
			want: "waiting: 2 runs are still working, 3 workspace shells are open",
		},
		{
			// A paused run looks running in `aether runs`, so it is named
			// even though it holds nothing back.
			name: "paused runs are named but excused",
			in:   &protocol.ServerUpdateWaiting{Runs: 1, Paused: 2},
			want: "waiting: 1 run is still working, 2 paused runs are not holding it back",
		},
		{
			name: "a newer server reporting something this client cannot name",
			in:   &protocol.ServerUpdateWaiting{},
			want: "waiting: the server did not say what for",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := waitingLine(tc.in); got != tc.want {
				t.Fatalf("waitingLine = %q, want %q", got, tc.want)
			}
		})
	}
}

// An incapable server prints the real reason and the exact commands, not a
// friendlier summary of them.
func TestPrintServerUpdateStatusIncapable(t *testing.T) {
	var out bytes.Buffer
	err := printServerUpdateStatus(&out, protocol.ServerUpdateStatusResult{
		ServerVersion:  "v0.1.0",
		Capable:        false,
		Incapable:      "its binary directory is not writable by the server process",
		ManualCommands: []string{"sudo aether update", "sudo systemctl restart aether-server"},
	})
	if err != nil {
		t.Fatalf("print: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"its binary directory is not writable by the server process",
		"sudo aether update",
		"sudo systemctl restart aether-server",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status %q is missing %q", got, want)
		}
	}
}

// A pending update always says what it is waiting for.
func TestPrintServerUpdateStatusPendingSaysWhatItWaitsFor(t *testing.T) {
	var out bytes.Buffer
	err := printServerUpdateStatus(&out, protocol.ServerUpdateStatusResult{
		ServerVersion: "v0.1.0",
		Capable:       true,
		Pending:       &protocol.PendingServerUpdate{Version: "v0.2.0", RequestedBy: "mem_1", RequestedAt: "2026-09-03T12:00:00Z"},
		Waiting:       &protocol.ServerUpdateWaiting{Runs: 1, Paused: 1},
	})
	if err != nil {
		t.Fatalf("print: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "pending update: v0.2.0") {
		t.Fatalf("status %q does not name the pending update", got)
	}
	if !strings.Contains(got, "1 run is still working") || !strings.Contains(got, "1 paused run is not holding it back") {
		t.Fatalf("status %q does not say what it is waiting for", got)
	}
}
