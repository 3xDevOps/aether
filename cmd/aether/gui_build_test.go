package main

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/localops"
)

// The gateway spawns `gui build --json` and reads stdout for phases, so a
// build that cannot start has to say so there too: without the error line
// the dashboard would poll a build that never began.
func TestGUIBuildJSONReportsAFailureAsAnErrorLine(t *testing.T) {
	// An invalid explicit CLI path must refuse the build before any download.
	missing := t.TempDir() + "/nowhere/aether"
	t.Setenv("AETHER_BIN", missing)
	var out bytes.Buffer

	err := guiBuildTo([]string{"--json", "--build-dir", t.TempDir()}, &out)
	if err == nil {
		t.Fatal("guiBuildTo succeeded without an installed CLI")
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("events = %q, want only the error event", out.String())
	}
	var got buildEvent
	if decodeErr := json.Unmarshal([]byte(lines[len(lines)-1]), &got); decodeErr != nil {
		t.Fatalf("last line %q: %v", lines[len(lines)-1], decodeErr)
	}
	if got.Phase != localops.PhaseError {
		t.Fatalf("phase = %q, want %q", got.Phase, localops.PhaseError)
	}
	if got.Error != err.Error() || !strings.Contains(got.Error, strconv.Quote(missing)) {
		t.Fatalf("error event = %q, want returned error %q naming %q", got.Error, err, missing)
	}
}

// Without --json nothing goes to the event stream: a terminal reads the
// build's own output, and a stray JSON line there would be noise.
func TestGUIBuildWithoutJSONPrintsNoEvents(t *testing.T) {
	t.Setenv("AETHER_BIN", t.TempDir()+"/nowhere/aether")
	var out bytes.Buffer

	if err := guiBuildTo([]string{"--build-dir", t.TempDir()}, &out); err == nil {
		t.Fatal("guiBuildTo succeeded without an installed CLI")
	}
	if out.Len() != 0 {
		t.Fatalf("events = %q, want none", out.String())
	}
}
