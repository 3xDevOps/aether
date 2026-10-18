package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestGUIBuildRejectsInconsistentStagingFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--stage", "--staged"}, "mutually exclusive"},
		{[]string{"--after-exit", "12"}, "needs --staged"},
		{[]string{"--staged", "--after-exit", "twelve"}, `"twelve" is not a process id`},
		{[]string{"--stage", "--zip", "app.zip"}, "cannot be staged"},
	} {
		err := guiBuildTo(tc.args, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("gui build %v: err = %v, want it to contain %q", tc.args, err, tc.want)
		}
	}
}

func TestStagedDesktopCopiesAndThenDeletesTheStagedApp(t *testing.T) {
	buildDir := t.TempDir()
	if _, _, err := stagedDesktop(context.Background(), buildDir); err == nil || !strings.Contains(err.Error(), "no staged desktop app") {
		t.Fatalf("err = %v, want a refusal when nothing was staged", err)
	}
	staged := filepath.Join(buildDir, localops.StagedAppDir)
	if err := os.MkdirAll(staged, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "aether-desktop.exe"), []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	src, release, err := stagedDesktop(context.Background(), buildDir)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "app")
	if err := src(dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "aether-desktop.exe")); string(got) != "new" {
		t.Fatalf("copied app = %q", got)
	}
	release()
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged app still there after release: %v", err)
	}
}
