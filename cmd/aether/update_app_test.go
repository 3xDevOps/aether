//go:build !windows

// The desktop-app rebuild `aether update` runs. The fixture stands in for
// `gui build` with a shell script, so this file is unix only; `aether
// update` refuses on Windows long before it gets here.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/localops"
)

// stubRebuild points rebuildDesktopAppTo at script, claims an app is
// installed, and answers running for whether that app has a live process.
func stubRebuild(t *testing.T, script string, running func() bool) {
	t.Helper()
	oldLookup, oldApp := lookupRealUser, installedDesktopApp
	oldArgv, oldRunning := rebuildAppArgv, desktopAppRunning
	t.Cleanup(func() {
		lookupRealUser, installedDesktopApp = oldLookup, oldApp
		rebuildAppArgv, desktopAppRunning = oldArgv, oldRunning
	})
	lookupRealUser = func() (localops.RealUser, error) {
		return localops.RealUser{Home: "/home/u"}, nil
	}
	installedDesktopApp = func(string, localops.RealUser) (string, bool) {
		return "/home/u/.local/share/aether/desktop", true
	}
	rebuildAppArgv = func(string, localops.RealUser, bool) []string { return []string{script} }
	desktopAppRunning = func(string, string) bool { return running() }
}

// script writes an executable shell script and returns its path.
func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gui-build")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// The install swap renames the running app's directory aside, so after the
// build the process no longer looks like it is running out of the app path
// and the restart line would never print. It has to be sampled first.
func TestRebuildDesktopAppSamplesTheRunningAppBeforeTheSwap(t *testing.T) {
	swapped := filepath.Join(t.TempDir(), "swapped")
	stubRebuild(t, script(t, "touch "+swapped+"\n"), func() bool {
		// Running until the build swaps the directory out from under it,
		// exactly as procExeUnder stops matching after the rename.
		_, err := os.Stat(swapped)
		return err != nil
	})
	var out bytes.Buffer

	if err := rebuildDesktopAppTo("/usr/local/bin/aether", &out); err != nil {
		t.Fatalf("rebuildDesktopAppTo: %v", err)
	}
	if _, err := os.Stat(swapped); err != nil {
		t.Fatalf("the build never ran: %v", err)
	}
	if !strings.Contains(out.String(), "restart the Aether app to use the new version") {
		t.Fatalf("output = %q, want the restart line", out.String())
	}
}

// Nothing to restart, nothing to say.
func TestRebuildDesktopAppSaysNothingAboutAnAppThatWasNotRunning(t *testing.T) {
	stubRebuild(t, script(t, "exit 0\n"), func() bool { return false })
	var out bytes.Buffer

	if err := rebuildDesktopAppTo("/usr/local/bin/aether", &out); err != nil {
		t.Fatalf("rebuildDesktopAppTo: %v", err)
	}
	if strings.Contains(out.String(), "restart the Aether app") {
		t.Fatalf("output = %q, want no restart line", out.String())
	}
}

// The CLI half of the update already landed, so the failure has to say so
// and name the command to rerun rather than read as a broken update.
func TestRebuildDesktopAppReportsAFailedBuild(t *testing.T) {
	stubRebuild(t, script(t, "exit 1\n"), func() bool { return true })
	var out bytes.Buffer

	err := rebuildDesktopAppTo("/usr/local/bin/aether", &out)
	if err == nil {
		t.Fatal("a failed build reported success")
	}
	if !strings.Contains(err.Error(), "aether is updated") {
		t.Fatalf("err = %v, want it to say the CLI update succeeded", err)
	}
	if !strings.Contains(err.Error(), "rerun it with:") {
		t.Fatalf("err = %v, want the rerun command", err)
	}
	if strings.Contains(out.String(), "restart the Aether app") {
		t.Fatalf("output = %q, want no restart line after a failed build", out.String())
	}
}
