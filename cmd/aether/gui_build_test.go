package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/localops"
)

// The gateway spawns `gui build --json` and reads stdout for phases, so a
// build that cannot start has to say so there too: without the error line
// the dashboard would poll a build that never began.
func TestGUIBuildJSONReportsAFailureAsAnErrorLine(t *testing.T) {
	// The shell refuses to build an app the CLI would not be found by, and
	// an AETHER_BIN that does not exist is exactly that refusal.
	t.Setenv("AETHER_BIN", t.TempDir()+"/nowhere/aether")
	var out bytes.Buffer

	err := guiBuildTo([]string{"--json", "--build-dir", t.TempDir()}, &out)
	if err == nil {
		t.Fatal("guiBuildTo succeeded without a CLI the app can find")
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var got buildEvent
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &got); err != nil {
		t.Fatalf("last line %q: %v", lines[len(lines)-1], err)
	}
	if got.Phase != localops.PhaseError {
		t.Fatalf("phase = %q, want %q", got.Phase, localops.PhaseError)
	}
	// The real error, not a friendlier one: it names where the shell looks.
	if !strings.Contains(got.Error, "aether is not installed where the desktop app looks") {
		t.Fatalf("error = %q, want the build's own message", got.Error)
	}
}

// Without --json nothing goes to the event stream: a terminal reads the
// build's own output, and a stray JSON line there would be noise.
func TestGUIBuildWithoutJSONPrintsNoEvents(t *testing.T) {
	t.Setenv("AETHER_BIN", t.TempDir()+"/nowhere/aether")
	var out bytes.Buffer

	if err := guiBuildTo([]string{"--build-dir", t.TempDir()}, &out); err == nil {
		t.Fatal("guiBuildTo succeeded without a CLI the app can find")
	}
	if out.Len() != 0 {
		t.Fatalf("events = %q, want none", out.String())
	}
}
