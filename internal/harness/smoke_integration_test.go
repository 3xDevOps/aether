//go:build integration

package harness

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
)

// Real-harness smoke tests: launch each shipped profile's actual CLI in
// both modes and verify the flags are accepted and output flows. They
// need images with the real agent CLIs installed (and login state for
// subscription harnesses), so each is gated on an env var naming its
// image: AETHER_SMOKE_IMAGE_CLAUDE, AETHER_SMOKE_IMAGE_CODEX,
// AETHER_SMOKE_IMAGE_OPENCODE. Unset = skipped,
// so plain `make test-integration` on a Docker-only host still passes;
// CI provides the images.

const smokeTask = "Reply with exactly the word pong and nothing else."

func smokeImage(t *testing.T, name string) string {
	t.Helper()
	img := os.Getenv("AETHER_SMOKE_IMAGE_" + strings.ToUpper(name))
	if img == "" {
		t.Skipf("AETHER_SMOKE_IMAGE_%s unset; real-harness smoke needs an image with the %s CLI", strings.ToUpper(name), name)
	}
	return img
}

// runSmoke launches argv in image on a TTY and returns the first chunk of
// output, failing if the process produces nothing before the deadline
// (unknown flags make these CLIs exit immediately with a usage error,
// which shows up in the output and fails the assertion in the caller).
func runSmoke(t *testing.T, image string, argv []string, env map[string]string) string {
	t.Helper()
	d, err := runtime.NewDocker(runtime.WithLabels(map[string]string{"aether.test": t.Name()}))
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	id, err := d.Create(ctx, runtime.Spec{
		Name:    fmt.Sprintf("smoke-%s-%d", strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")), time.Now().UnixNano()),
		Image:   image,
		Env:     env,
		Command: argv,
		TTY:     true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		_ = d.Destroy(dctx, id)
	})
	att, err := d.Attach(ctx, id)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer att.Close()
	if err := d.Start(ctx, id); err != nil {
		t.Fatalf("Start: %v", err)
	}

	chunks := make(chan string, 64)
	go func() {
		defer close(chunks)
		buf := make([]byte, 64<<10)
		for {
			n, err := att.Stdout().Read(buf)
			if n > 0 {
				chunks <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	// Accumulate until the stream ends, the output settles (2s quiet after
	// the first chunk), or the overall deadline hits. TUIs never exit, so
	// quiescence is the normal path.
	var b strings.Builder
	first := time.After(2 * time.Minute)
	for {
		var quiet <-chan time.Time
		if b.Len() > 0 {
			quiet = time.After(2 * time.Second)
		}
		select {
		case chunk, ok := <-chunks:
			if !ok {
				return b.String()
			}
			b.WriteString(chunk)
			if b.Len() >= 32<<10 {
				return b.String()
			}
		case <-quiet:
			return b.String()
		case <-first:
			t.Fatal("no output before deadline")
		case <-ctx.Done():
			t.Fatal("context deadline before output")
		}
	}
}

// assertNoUsageError fails when the harness rejected its flags: every
// shipped CLI prints a recognizable usage/unknown-flag error and exits.
func assertNoUsageError(t *testing.T, name, mode, output string) {
	t.Helper()
	if output == "" {
		t.Fatalf("%s %s: no output", name, mode)
	}
	lower := strings.ToLower(output)
	for _, marker := range []string{"unknown option", "unknown flag", "unrecognized argument", "unexpected argument", "no such option", "usage:"} {
		if strings.Contains(lower, marker) {
			t.Fatalf("%s %s: flags rejected:\n%s", name, mode, output)
		}
	}
}

func smokeBothModes(t *testing.T, name string, env map[string]string) {
	image := smokeImage(t, name)
	p, ok := Lookup(name)
	if !ok {
		t.Fatalf("Lookup(%q) missing", name)
	}
	t.Run("tui", func(t *testing.T) {
		out := runSmoke(t, image, Argv(p.TUIArgs, smokeTask), env)
		assertNoUsageError(t, name, "tui", out)
	})
	t.Run("headless", func(t *testing.T) {
		out := runSmoke(t, image, Argv(p.HeadlessArgs, smokeTask), env)
		assertNoUsageError(t, name, "headless", out)
	})
	if p.MCPConfigFlag == "" {
		return
	}
	// Conflict coordination appends the MCP registration after the task
	// prompt, so the flag lands behind a positional argument - in both
	// launch modes, whose templates differ structurally. Every CLI tested
	// accepts that today; this is what turns a future parser change into
	// one failing test instead of every run silently degrading to
	// notice-only. The config path need not exist - a CLI that rejects the
	// option says so before it ever opens the file.
	for mode, template := range map[string][]string{"tui": p.TUIArgs, "headless": p.HeadlessArgs} {
		t.Run("mcp-config-"+mode, func(t *testing.T) {
			argv := append(Argv(template, smokeTask), p.MCPArgs("/run/aether/mcp.json")...)
			assertNoUsageError(t, name, "mcp-config-"+mode, runSmoke(t, image, argv, env))
		})
	}
}

func passthroughEnv(p Profile) map[string]string {
	env := map[string]string{"TERM": "xterm-256color"}
	for _, k := range p.EnvPassthrough {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	return env
}

func TestSmokeClaude(t *testing.T) {
	p, _ := Lookup("claude")
	smokeBothModes(t, "claude", passthroughEnv(p))
}

func TestSmokeOpencode(t *testing.T) {
	p, _ := Lookup("opencode")
	smokeBothModes(t, "opencode", passthroughEnv(p))
}

// Codex flags are verified independently of the shared harness: its
// headless mode must emit JSON lines, pinning `exec --json`.
func TestSmokeCodexFlags(t *testing.T) {
	image := smokeImage(t, "codex")
	p, _ := Lookup("codex")
	env := passthroughEnv(p)

	t.Run("tui", func(t *testing.T) {
		out := runSmoke(t, image, Argv(p.TUIArgs, smokeTask), env)
		assertNoUsageError(t, "codex", "tui", out)
	})
	t.Run("headless-json", func(t *testing.T) {
		out := runSmoke(t, image, Argv(p.HeadlessArgs, smokeTask), env)
		assertNoUsageError(t, "codex", "headless", out)
		if !strings.Contains(out, "{") {
			t.Fatalf("codex headless produced no JSON:\n%s", out)
		}
	})
}
