//go:build integration

package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
)

// TestACPAdapterInstall installs each pinned adapter the way InstallCommand
// does, into a member home mounted at /home/aether in the standard image
// and as a non-root run user, then starts it and completes the ACP
// initialize handshake over stdio. The cold start it logs is the first start
// after install, which is what a member's first enhanced run pays.
//
// AETHER_ACP_IMAGE names the standard image (docs/testing.md). Unset, or
// without access to the npm registry, the test skips.
func TestACPAdapterInstall(t *testing.T) {
	image := os.Getenv("AETHER_ACP_IMAGE")
	if image == "" {
		t.Skip("AETHER_ACP_IMAGE unset; the adapter install test needs the standard image")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not found")
	}
	for _, name := range []string{"claude", "codex"} {
		t.Run(name, func(t *testing.T) {
			p, _ := Lookup(name)
			testAdapterInstall(t, image, p)
		})
	}
}

func testAdapterInstall(t *testing.T, image string, p Profile) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	home := t.TempDir()
	// The run user is not the host user that owns the temporary directory.
	if err := os.Chmod(home, 0o777); err != nil {
		t.Fatal(err)
	}
	docker := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	container := fmt.Sprintf("aether-acp-%s-%d", p.Name, time.Now().UnixNano())
	if out, err := docker("run", "-d", "--name", container, "--label", "aether.test="+t.Name(),
		"--user", "1000:1000", "--env", "HOME=/home/aether", "--volume", home+":/home/aether",
		"--workdir", "/home/aether", image, "sleep", "900"); err != nil {
		t.Fatalf("start container: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		// The run user owns what it installed, and the host user that
		// removes the temporary directory may not be able to.
		_ = exec.Command("docker", "exec", container, "find", "/home/aether", "-mindepth", "1", "-delete").Run()
		_ = exec.Command("docker", "rm", "-f", container).Run()
	})
	shell := func(script string) (string, error) {
		return docker("exec", container, "/bin/sh", "-c", script)
	}
	install := p.ACPInstall
	if out, err := shell("npm view " + install.Package + "@" + install.Version + " version --fetch-retries=0"); err != nil {
		t.Skipf("the npm registry is unreachable from the container: %v\n%s", err, out)
	}
	if out, err := shell(install.installLine()); err != nil {
		t.Fatalf("install %s: %v\n%s", install.Package, err, out)
	}
	resolved, err := shell(`PATH="$HOME/.local/bin:$PATH" command -v ` + p.ACPArgs[0])
	if want := "/home/aether/.local/bin/" + install.Binary; err != nil || resolved != want {
		t.Fatalf("%s resolves to %q (%v), want %s", p.ACPArgs[0], resolved, err, want)
	}

	cmd := exec.CommandContext(ctx, "docker", append([]string{"exec", "-i",
		"--env", "CLAUDE_CONFIG_DIR=/home/aether/.claude", "--env", "NO_BROWSER=1",
		container, resolved}, p.ACPArgs[1:]...)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	started := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false}}}` + "\n"
	if _, err := stdin.Write([]byte(initialize)); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for lines.Scan() {
		var reply struct {
			ID     int `json:"id"`
			Result struct {
				ProtocolVersion int `json:"protocolVersion"`
			} `json:"result"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(lines.Bytes(), &reply) != nil || reply.ID != 1 {
			continue
		}
		if reply.Error != nil || reply.Result.ProtocolVersion != 1 {
			t.Fatalf("initialize = %s", lines.Bytes())
		}
		t.Logf("%s@%s cold start: initialize answered in %d ms", install.Package, install.Version, time.Since(started).Milliseconds())
		if os.Getenv("ACP_LIVE") == "1" {
			testLiveSession(ctx, t, container, resolved, p)
		}
		return
	}
	t.Fatalf("%s exited without answering initialize: %v\nstderr: %s", install.Binary, lines.Err(), stderr.String())
}

// testLiveSession opens a session the way an enhanced run does: the session
// host over the adapter's stdio in the standard image, initialize then
// session/new. No prompt is sent, so no login is needed.
func testLiveSession(ctx context.Context, t *testing.T, container, adapter string, p Profile) {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"exec", "-i",
		"--env", "CLAUDE_CONFIG_DIR=/home/aether/.claude", "--env", "NO_BROWSER=1",
		container, adapter}, p.ACPArgs[1:]...)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	s, err := acphost.Start(ctx, stdout, stdin, acphost.Config{
		LogPath: filepath.Join(t.TempDir(), "run.items.jsonl"),
		Cwd:     "/home/aether",
	})
	if err != nil {
		// A logged-out Codex fails session/new; initialize still ran.
		if strings.Contains(err.Error(), "session/new") && strings.Contains(err.Error(), "Authentication required") {
			t.Skipf("agent not logged in: %v", err)
		}
		t.Fatalf("open the session: %v\nstderr: %s", err, stderr.String())
	}
	if s.SessionID() == "" || s.State().Mode == "" {
		t.Fatalf("session %q state %+v", s.SessionID(), s.State())
	}
	t.Logf("%s session %s in mode %s", p.Name, s.SessionID(), s.State().Mode)
	_ = s.Close()
}
