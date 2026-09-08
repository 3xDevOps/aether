//go:build integration

package harness

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
)

// Real-harness smoke tests: launch each shipped profile's actual CLI and
// verify the argv Aether ships is still the argv that CLI accepts. Each is
// gated on an env var naming an image, and there are two kinds:
//
//   - AETHER_SMOKE_IMAGE_<NAME> carries the CLI *and that harness's login
//     state*, so the agent can be driven far enough to produce output. No
//     public CI job has those credentials; run these by hand.
//   - AETHER_SMOKE_IMAGE_<NAME>_NOLOGIN carries the CLI and nothing else.
//     TestSmokeHeadlessNoLogin needs exactly that - it asserts the run dies
//     of missing credentials rather than of argument parsing - so this is
//     the one CI sets, from the image built by images/smoke/Dockerfile.
//
// Unset = skipped, so plain `make test-integration` on a Docker-only host
// still passes. See docs/testing.md to run them locally.

const smokeTask = "Reply with exactly the word pong and nothing else."

// smokeImage resolves one gate variable. It returns false rather than
// skipping so a caller can decide whether a missing image skips the whole
// test: a parent whose every subtest skipped must report skipped, not the
// PASS that hid this suite's absence from CI.
func smokeImage(name, suffix string) (string, bool) {
	img := os.Getenv("AETHER_SMOKE_IMAGE_" + strings.ToUpper(name) + suffix)
	return img, img != ""
}

func requireSmokeImage(t *testing.T, name string) string {
	t.Helper()
	img, ok := smokeImage(name, "")
	if !ok {
		t.Skipf("AETHER_SMOKE_IMAGE_%s unset; real-harness smoke needs an image with the %s CLI and its login state", strings.ToUpper(name), name)
	}
	return img
}

// runSmoke launches argv in image on a TTY and returns the first chunk of
// output, giving up once the output settles: a TUI never exits, so
// quiescence is the only way back. A CLI that refuses its arguments has
// already printed the refusal by then.
func runSmoke(t *testing.T, image string, argv []string, env map[string]string) string {
	t.Helper()
	out, _ := smokeRun(t, image, argv, env, false)
	return out
}

// runSmokeToExit reads argv's output to the end and returns it with the
// process exit code. Every headless launch exits on its own, and only by
// waiting for that is the code meaningful - stopping at quiescence would
// report "still running" whenever an agent paused mid-answer for longer
// than the quiet window.
func runSmokeToExit(t *testing.T, image string, argv []string, env map[string]string) (string, int) {
	t.Helper()
	return smokeRun(t, image, argv, env, true)
}

// exitStillRunning is the code reported for a process that had not exited
// when smokeRun stopped reading it.
const exitStillRunning = -1

func smokeRun(t *testing.T, image string, argv []string, env map[string]string, waitForExit bool) (string, int) {
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
	var b strings.Builder
	first := time.After(2 * time.Minute)
	for {
		// Quiescence only ends the read when the caller is not waiting for
		// an exit code; 2s after the first chunk is enough for a CLI that
		// refuses its arguments to have said so.
		var quiet <-chan time.Time
		if b.Len() > 0 && !waitForExit {
			quiet = time.After(2 * time.Second)
		}
		select {
		case chunk, ok := <-chunks:
			if !ok {
				// The stream closed, so the process is gone and Wait
				// returns its code without blocking.
				status, waitErr := d.Wait(ctx, id)
				if waitErr != nil {
					t.Fatalf("Wait: %v", waitErr)
				}
				return b.String(), status.Code
			}
			// Keep draining past the cap so a chatty agent still reaches
			// its exit rather than stalling on a full pipe.
			if b.Len() < 32<<10 {
				b.WriteString(chunk)
			}
			if b.Len() >= 32<<10 && !waitForExit {
				return b.String(), exitStillRunning
			}
		case <-quiet:
			return b.String(), exitStillRunning
		case <-first:
			t.Fatal("no output before deadline")
		case <-ctx.Done():
			t.Fatal("context deadline before output")
		}
	}
}

// argvRejections are the ways a CLI refuses the argv it was handed. An
// unknown flag is only half of it: a parser also refuses a combination of
// flags it understands individually, which is how Claude Code started
// rejecting "--output-format stream-json" without "--verbose" and broke
// every headless claude run. Three CLIs written in three languages phrase
// this three ways and none of them promises to keep its wording, so the
// list is broad on purpose: a phrasing added early costs nothing, and a
// missing one costs a release.
var argvRejections = []string{
	"unknown option", "unknown flag", "unknown argument", "unknown command",
	"unrecognized argument", "unrecognized option", "invalid option",
	"unexpected argument", "no such option", "usage:",
	"flag provided but not defined",
	// A refused flag combination, which is how Claude Code broke every
	// headless run, and how opencode says it: yargs prints its whole help
	// table and no error sentence at all, so the help table is the error.
	"requires --", "only works with --", "show help",
}

// noLoginProof is what a harness must show, with no credentials anywhere,
// to prove it accepted the shipped argv and got as far as real work. A
// refused command line shows none of it.
type noLoginProof struct {
	// output holds substrings, any one of which is that proof.
	output []string
	// cleanExit accepts a zero exit code as the same proof, for a harness
	// that can finish the task with no login at all.
	cleanExit bool
}

// Claude Code and codex must authenticate, so being turned away by the
// provider is the proof: only a CLI that parsed its argv gets that far.
// opencode needs no login of its own - it answers from a bundled provider -
// so it proves the same thing by finishing, or, when that provider is
// unreachable, by still printing the session header it prints only once the
// command is accepted.
var noLoginProofs = map[string]noLoginProof{
	"claude":   {output: []string{"authentication_failed", "not logged in", "invalid api key"}},
	"codex":    {output: []string{"401", "unauthorized"}},
	"opencode": {output: []string{"> build"}, cleanExit: true},
}

// assertArgvAccepted fails when the harness rejected its flags: every
// shipped CLI prints a recognizable rejection and exits.
func assertArgvAccepted(t *testing.T, name, mode, output string) {
	t.Helper()
	if output == "" {
		t.Fatalf("%s %s: no output", name, mode)
	}
	lower := strings.ToLower(output)
	for _, marker := range argvRejections {
		if strings.Contains(lower, marker) {
			t.Fatalf("%s %s: flags rejected:\n%s", name, mode, output)
		}
	}
}

func smokeBothModes(t *testing.T, name string, env map[string]string) {
	image := requireSmokeImage(t, name)
	p, ok := Lookup(name)
	if !ok {
		t.Fatalf("Lookup(%q) missing", name)
	}
	t.Run("tui", func(t *testing.T) {
		out := runSmoke(t, image, Argv(p.TUIArgs, smokeTask), env)
		assertArgvAccepted(t, name, "tui", out)
	})
	t.Run("headless", func(t *testing.T) {
		out := runSmoke(t, image, Argv(p.HeadlessArgs, smokeTask), env)
		assertArgvAccepted(t, name, "headless", out)
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
			assertArgvAccepted(t, name, "mcp-config-"+mode, runSmoke(t, image, argv, env))
		})
	}
}

// launchEnv is what the scheduler puts in every container regardless of
// credentials: TERM and the harness's own launch requirements
// (Profile.Env). Setting anything here that a real run does not get would
// make these tests prove something about a container Aether never starts.
func launchEnv(p Profile) map[string]string {
	env := map[string]string{"TERM": "xterm-256color"}
	maps.Copy(env, p.Env)
	return env
}

func passthroughEnv(p Profile) map[string]string {
	env := launchEnv(p)
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
	image := requireSmokeImage(t, "codex")
	p, _ := Lookup("codex")
	env := passthroughEnv(p)

	t.Run("tui", func(t *testing.T) {
		out := runSmoke(t, image, Argv(p.TUIArgs, smokeTask), env)
		assertArgvAccepted(t, "codex", "tui", out)
	})
	t.Run("headless-json", func(t *testing.T) {
		out := runSmoke(t, image, Argv(p.HeadlessArgs, smokeTask), env)
		assertArgvAccepted(t, "codex", "headless", out)
		if !strings.Contains(out, "{") {
			t.Fatalf("codex headless produced no JSON:\n%s", out)
		}
	})
}

// TestSmokeHeadlessNoLogin is the drift guard: it runs each shipped
// headless argv against the current CLI with no credentials at all and
// requires the run to fail for want of a login, never for want of a
// parseable command line. A vendor tightening its parser fails here instead
// of in every run.
//
// It is the one smoke test CI can run, because it is the one that needs no
// login state; .github/workflows/ci.yml builds images/smoke/Dockerfile and
// points the _NOLOGIN variables at it.
func TestSmokeHeadlessNoLogin(t *testing.T) {
	names := slices.Sorted(maps.Keys(noLoginProofs))
	if !slices.ContainsFunc(names, func(n string) bool { _, ok := smokeImage(n, "_NOLOGIN"); return ok }) {
		t.Skip("no AETHER_SMOKE_IMAGE_*_NOLOGIN set; the argv drift guard needs an image with the agent CLIs and no credentials (docs/testing.md)")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			image, ok := smokeImage(name, "_NOLOGIN")
			if !ok {
				t.Skipf("AETHER_SMOKE_IMAGE_%s_NOLOGIN unset", strings.ToUpper(name))
			}
			p, found := Lookup(name)
			if !found {
				t.Fatalf("Lookup(%q) missing", name)
			}
			want := noLoginProofs[name]
			out, exit := runSmokeToExit(t, image, Argv(p.HeadlessArgs, smokeTask), launchEnv(p))
			assertArgvAccepted(t, name, "headless-no-login", out)
			lower := strings.ToLower(out)
			started := slices.ContainsFunc(want.output, func(m string) bool { return strings.Contains(lower, m) })
			if !started && !(want.cleanExit && exit == 0) {
				t.Fatalf("%s headless with no login never got past its own argument parsing (exit %d):\n%s", name, exit, out)
			}
		})
	}
}
