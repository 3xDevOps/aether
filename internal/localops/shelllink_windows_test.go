//go:build windows

package localops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestShellLinkLaunchesNativeConsumer(t *testing.T) {
	root := t.TempDir()
	workingDir := filepath.Join(root, "shortcut target & José")
	linkDir := filepath.Join(root, "Start Menu & Ł")
	launcherDir := filepath.Join(root, "launcher cwd")
	for _, dir := range []string{workingDir, linkDir, launcherDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	const markerName = "shell-link-marker.txt"
	const markerContents = "native shell link fixture"
	fixtureSource := filepath.Join(root, "shell-link-fixture.go")
	fixture := filepath.Join(workingDir, "fixture & 日本.exe")
	source := `package main

import "os"

func main() {
	if err := os.WriteFile("shell-link-marker.txt", []byte("native shell link fixture"), 0o644); err != nil {
		panic(err)
	}
}
`
	if err := os.WriteFile(fixtureSource, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	goExe, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("locate Go for shortcut fixture: %v", err)
	}
	buildCtx, cancelBuild := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancelBuild()
	build := exec.CommandContext(buildCtx, goExe, "build", "-o", fixture, fixtureSource)
	build.Dir = root
	build.Env = append(os.Environ(), "GO111MODULE=off", "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build shortcut fixture: %v\n%s", err, output)
	}

	shortcut := filepath.Join(linkDir, "Aether & 日本.lnk")
	if err := writeShellLink(shortcut, fixture, workingDir); err != nil {
		t.Fatalf("writeShellLink: %v", err)
	}

	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatalf("locate Windows PowerShell for shortcut consumer: %v", err)
	}
	script := filepath.Join(root, "launch-shortcut.ps1")
	const scriptContents = "$ErrorActionPreference = 'Stop'\n$process = Start-Process -FilePath $args[0] -Wait -PassThru\nif ($null -eq $process) { throw 'Start-Process returned no process' }\nif ($process.ExitCode -ne 0) { exit $process.ExitCode }\n"
	if err := os.WriteFile(script, []byte(scriptContents), 0o644); err != nil {
		t.Fatal(err)
	}

	launchCtx, cancelLaunch := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancelLaunch()
	launch := exec.CommandContext(launchCtx, powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", script, shortcut)
	launch.Dir = launcherDir
	launch.WaitDelay = 2 * time.Second
	if output, err := launch.CombinedOutput(); err != nil {
		t.Fatalf("launch shortcut through Start-Process: %v\n%s", err, output)
	}

	contents, err := os.ReadFile(filepath.Join(workingDir, markerName))
	if err != nil {
		t.Fatalf("read marker in shortcut working directory: %v", err)
	}
	if string(contents) != markerContents {
		t.Fatalf("fixture marker = %q, want %q", contents, markerContents)
	}
}
