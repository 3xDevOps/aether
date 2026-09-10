// The gh check the GitHub step makes before it prints a login command: what
// the member's own container answers about its gh, and what has to happen
// when that gh cannot do the login.

package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// The GitHub step probes before it prints the login command, so the screen
// can replace that command with the one that fixes the environment.
func TestProbeGitHubCLI(t *testing.T) {
	for _, tc := range []struct {
		name     string
		saved    string
		standard string
		// pinned marks a standard image an operator set, rather than the
		// one this build ships.
		pinned  bool
		code    int
		out     string
		status  domain.GitHubCLIStatus
		version string
		// detail, when set, is what the answer must carry beyond out.
		detail string
		// where is what the container answers about whose gh that is;
		// empty means one the image supplies.
		where  string
		path   string
		remedy string
		admin  string
	}{
		{
			name:    "current standard image",
			out:     ghVersionCurrent,
			status:  domain.GitHubCLIOK,
			version: "2.100.0",
		},
		{
			// The whole feature turns on this one: a gh at exactly the
			// minimum answers the login check.
			name:    "gh at exactly the minimum",
			out:     "gh version 2.81.0 (2026-01-08)\n",
			status:  domain.GitHubCLIOK,
			version: "2.81.0",
		},
		{
			// A gh the member installed into their own home comes first
			// on PATH and outlives every image, so no image remedy can
			// reach it and none is offered.
			name:    "gh the member installed in their own home",
			out:     ghVersionUbuntu,
			where:   ghWhere("/root/.local/bin/gh"),
			status:  domain.GitHubCLIOutdated,
			version: "2.45.0",
			path:    "/root/.local/bin/gh",
			remedy:  "rm /root/.local/bin/gh",
		},
		{
			name:   "standard image without gh",
			code:   127,
			out:    ghNotFound,
			status: domain.GitHubCLIMissing,
			remedy: "aether terminal stop",
			admin:  "docker pull ",
		},
		{
			name:   "saved environment without gh",
			saved:  "aether/member-test:1",
			code:   127,
			out:    ghNotFound,
			status: domain.GitHubCLIMissing,
			remedy: "aether env reset",
		},
		{
			name:    "gh too old for the login check",
			out:     ghVersionUbuntu,
			status:  domain.GitHubCLIOutdated,
			version: "2.45.0",
			remedy:  "aether terminal stop",
			admin:   "docker pull ",
		},
		{
			// gh is there and exits non-zero. Calling that "no gh" would
			// send the member to replace an environment that has one.
			name:   "gh that will not run",
			code:   1,
			out:    "gh: error while loading shared libraries",
			status: domain.GitHubCLIBroken,
			detail: "gh --version exited 1",
			remedy: "aether terminal stop",
			admin:  "docker pull ",
		},
		{
			// 127 is an executable that is not there; 126 is one that is
			// and would not run, which is not a missing gh.
			name:   "gh that is not executable",
			code:   126,
			out:    `OCI runtime exec failed: exec: "gh": permission denied`,
			status: domain.GitHubCLIBroken,
			detail: "gh --version exited 126",
			remedy: "aether terminal stop",
			admin:  "docker pull ",
		},
		{
			name:     "release-tagged standard image this build ships",
			standard: "ghcr.io/3xdevops/aether-standard:v0.2.0-alpha.5",
			code:     127,
			out:      ghNotFound,
			status:   domain.GitHubCLIMissing,
			remedy:   "aether terminal stop",
			admin:    "aether server update",
		},
		{
			// An operator who pinned --standard-image keeps that value
			// across a server update, so the pin is what has to move.
			name:     "release-tagged standard image the operator pinned",
			standard: "ghcr.io/3xdevops/aether-standard:v0.2.0-alpha.5",
			pinned:   true,
			code:     127,
			out:      ghNotFound,
			status:   domain.GitHubCLIMissing,
			remedy:   "aether terminal stop",
			admin:    "sudo aether-server config set standard-image <a newer image> && sudo systemctl restart aether-server",
		},
		{
			// A release tag on somebody else's registry is not moved by
			// an Aether release either, so the pin is what has to move.
			name:     "release-tagged image somewhere else",
			standard: "localhost:5000/aether-standard:v1.2.3",
			pinned:   true,
			code:     127,
			out:      ghNotFound,
			status:   domain.GitHubCLIMissing,
			remedy:   "aether terminal stop",
			admin:    "sudo aether-server config set standard-image <a newer image> && sudo systemctl restart aether-server",
		},
		{
			// A tag an operator rebuilds in place is repulled where the
			// server can see it.
			name:     "moving tag the operator pinned",
			standard: "acme/dev-environment:latest",
			pinned:   true,
			code:     127,
			out:      ghNotFound,
			status:   domain.GitHubCLIMissing,
			remedy:   "aether terminal stop",
			admin:    "docker pull acme/dev-environment:latest",
		},
		{
			name:     "digest-pinned standard image",
			standard: "ghcr.io/3xdevops/aether-standard@sha256:" + strings.Repeat("a", 64),
			code:     127,
			out:      ghNotFound,
			status:   domain.GitHubCLIMissing,
			remedy:   "aether terminal stop",
			admin:    "sudo aether-server config set standard-image <a newer image> && sudo systemctl restart aether-server",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, func(c *Config) {
				if tc.standard != "" {
					c.StandardImage = tc.standard
				}
				// The build default matches the configured image unless
				// the case is an operator's own pin.
				c.DefaultStandardImage = c.StandardImage
				if tc.pinned {
					c.DefaultStandardImage = "ghcr.io/3xdevops/aether-standard:v0.9.9"
				}
			})
			if tc.saved != "" {
				if err := e.db.UpdateMemberImage(t.Context(), e.member.ID, tc.saved); err != nil {
					t.Fatalf("UpdateMemberImage: %v", err)
				}
				if err := e.rt.Commit(t.Context(), "c-saved", tc.saved); err != nil {
					t.Fatalf("seed the saved image: %v", err)
				}
			}
			e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
				switch {
				case slices.Equal(argv, []string{"gh", "--version"}):
					return tc.code, tc.out, nil
				case asksWhereGhIs(argv):
					if tc.where == "" {
						return 0, ghWhere("/usr/local/bin/gh"), nil
					}
					return 0, tc.where, nil
				}
				t.Errorf("probe exec = %v, want gh --version or the path query", argv)
				return 0, "", nil
			}
			if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
				t.Fatalf("EnsureTerminal: %v", err)
			}
			cli, err := e.sched.ProbeGitHubCLI(t.Context(), e.member.ID)
			if err != nil {
				t.Fatalf("ProbeGitHubCLI: %v", err)
			}
			if cli.Status != tc.status || cli.Version != tc.version {
				t.Errorf("probe = %+v, want status %q version %q", cli, tc.status, tc.version)
			}
			if cli.Minimum != versionString(minGitHubCLI) {
				t.Errorf("minimum = %q, want %q", cli.Minimum, versionString(minGitHubCLI))
			}
			for _, want := range []string{strings.TrimSpace(tc.out), tc.detail} {
				if want != "" && !strings.Contains(cli.Detail, want) {
					t.Errorf("detail = %q, want it to carry %q", cli.Detail, want)
				}
			}
			if cli.Path != tc.path {
				t.Errorf("path = %q, want %q", cli.Path, tc.path)
			}
			if cli.Remedy != tc.remedy {
				t.Errorf("remedy = %q, want %q", cli.Remedy, tc.remedy)
			}
			admin := tc.admin
			if admin == "docker pull " {
				admin += e.cfg.StandardImage
			}
			if cli.AdminRemedy != admin {
				t.Errorf("admin remedy = %q, want %q", cli.AdminRemedy, admin)
			}
			if cli.SavedImage != tc.saved {
				t.Errorf("saved image = %q, want %q", cli.SavedImage, tc.saved)
			}
			if cli.Image == "" {
				t.Error("probe named no image for the terminal container")
			}
		})
	}
}

// gh writes its version to stdout and a distribution can warn on stderr;
// the answer a member reads carries both, on separate lines.
func TestProbeGitHubCLIJoinsBothStreams(t *testing.T) {
	e := newTestEnv(t, nil)
	e.rt.execStderr = "warning: gh is out of date"
	e.rt.execHandler = func(_ runtime.ID, _ []string) (int, string, error) {
		return 0, ghVersionCurrent, nil
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	cli, err := e.sched.ProbeGitHubCLI(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("ProbeGitHubCLI: %v", err)
	}
	if want := "v2.100.0\nwarning: gh is out of date"; !strings.HasSuffix(cli.Detail, want) {
		t.Errorf("detail = %q, want it to end with %q", cli.Detail, want)
	}
}

// the same gh; the way out is theirs either way, not a reopen that hands
// back the same filesystem.
func TestProbeGitHubCLIKeepsASavedEnvironmentOnItsOwnRemedy(t *testing.T) {
	e := newTestEnv(t, nil)
	e.rt.execHandler = func(_ runtime.ID, _ []string) (int, string, error) {
		return 127, ghNotFound, nil
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	// The member saved an environment; the container they are in still
	// runs the image it started from.
	if err := e.db.UpdateMemberImage(t.Context(), e.member.ID, "aether/member-test:2"); err != nil {
		t.Fatalf("UpdateMemberImage: %v", err)
	}
	if err := e.rt.Commit(t.Context(), "c-saved", "aether/member-test:2"); err != nil {
		t.Fatalf("seed the saved image: %v", err)
	}
	cli, err := e.sched.ProbeGitHubCLI(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("ProbeGitHubCLI: %v", err)
	}
	if cli.Remedy != "aether env reset" || cli.AdminRemedy != "" {
		t.Errorf("remedy = (%q, %q), want the member's own way out", cli.Remedy, cli.AdminRemedy)
	}
}

// A container left behind by a standard image that has already moved needs
// only a reopen; nothing recreates it while it runs.
func TestProbeGitHubCLISendsAStaleContainerToReopen(t *testing.T) {
	e := newTestEnv(t, nil)
	e.rt.execHandler = func(_ runtime.ID, _ []string) (int, string, error) {
		return 127, ghNotFound, nil
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	// The container the server is supervising predates a standard image
	// that has since moved, which is what a server update leaves behind.
	e.sched.mu.Lock()
	e.sched.terminals[e.member.ID].image = "ghcr.io/3xdevops/aether-standard:v0.1.0"
	e.sched.mu.Unlock()

	cli, err := e.sched.ProbeGitHubCLI(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("ProbeGitHubCLI: %v", err)
	}
	if cli.Remedy != "aether terminal stop" || cli.AdminRemedy != "" {
		t.Errorf("remedy = (%q, %q), want the member to reopen the terminal", cli.Remedy, cli.AdminRemedy)
	}
	// The field names the container's image, which is the whole point
	// here: the configured one has already moved on.
	if cli.Image != "ghcr.io/3xdevops/aether-standard:v0.1.0" {
		t.Errorf("image = %q, want the one the container is running", cli.Image)
	}
}

// A container that goes away under the probe - the shell exited, and
// supervision destroyed it - has a name of its own, so the screen can say
// "open the terminal first" instead of showing an internal error.
func TestProbeGitHubCLIReportsAContainerThatWentAway(t *testing.T) {
	e := newTestEnv(t, nil)
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	e.rt.execHandler = func(_ runtime.ID, _ []string) (int, string, error) {
		e.sched.mu.Lock()
		delete(e.sched.terminals, e.member.ID)
		e.sched.mu.Unlock()
		return 0, "", errors.New("exec create: No such container")
	}
	_, err := e.sched.ProbeGitHubCLI(t.Context(), e.member.ID)
	if !errors.Is(err, ErrTerminalNotRunning) {
		t.Fatalf("ProbeGitHubCLI error = %v, want %v", err, ErrTerminalNotRunning)
	}
}

// A container stopped under the exec answers an exit code, not an error,
// and a member who stops and reopens the terminal leaves a different one
// behind. Either way the answer describes a container they no longer have,
// including when that answer was a clean one.
func TestProbeGitHubCLIReportsATerminalReplacedUnderIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		out  string
	}{
		// What Docker answers for an exec whose container was stopped
		// under it: a code, no error, nothing on either stream.
		{name: "the container was stopped under it", code: 137},
		{name: "gh answered before it was", out: ghVersionCurrent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
				t.Fatalf("EnsureTerminal: %v", err)
			}
			e.rt.execHandler = func(_ runtime.ID, _ []string) (int, string, error) {
				e.sched.mu.Lock()
				e.sched.terminals[e.member.ID] = &terminalSupervision{member: e.member.ID, containerID: "c-reopened"}
				e.sched.mu.Unlock()
				return tc.code, tc.out, nil
			}
			_, err := e.sched.ProbeGitHubCLI(t.Context(), e.member.ID)
			if !errors.Is(err, ErrTerminalNotRunning) {
				t.Fatalf("ProbeGitHubCLI error = %v, want %v", err, ErrTerminalNotRunning)
			}
		})
	}
}

// Resolving an environment plan resolves the image, and resolving an image
// pulls one the daemon does not hold - a several hundred megabyte download
// inside the probe's own budget, on the call the member's dock is riding.
// The probe must answer without one, so it answers here for a member whose
// recorded image no plan could be built for.
func TestProbeGitHubCLINeedsNoEnvironmentPlan(t *testing.T) {
	e := newTestEnv(t, nil)
	e.rt.execHandler = func(_ runtime.ID, _ []string) (int, string, error) {
		return 0, ghVersionCurrent, nil
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	if err := e.db.UpdateMemberImage(t.Context(), e.member.ID, "aether/member-test:gone"); err != nil {
		t.Fatalf("UpdateMemberImage: %v", err)
	}
	saved, err := e.db.GetMember(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if _, err := e.sched.BuildEnvironmentPlan(t.Context(), nil, nil, saved, harness.Profile{}, EnvironmentPurposeTerminal); err == nil {
		t.Fatal("a plan for a missing image built; this test no longer proves anything")
	}
	cli, probeErr := e.sched.ProbeGitHubCLI(t.Context(), e.member.ID)
	if probeErr != nil {
		t.Fatalf("ProbeGitHubCLI: %v", probeErr)
	}
	if cli.Status != domain.GitHubCLIOK {
		t.Errorf("status = %q, want the probe to answer without a plan", cli.Status)
	}
}

// The probe holds no terminal lock and gives up on its own deadline; the
// gateway carrying it drops the member's connection at sixty seconds.
func TestProbeGitHubCLIStopsAtItsDeadline(t *testing.T) {
	e := newTestEnv(t, nil)
	released := make(chan struct{})
	t.Cleanup(func() { close(released) })
	e.rt.execHandler = func(_ runtime.ID, _ []string) (int, string, error) {
		<-released
		return 0, ghVersionCurrent, nil
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	// The gateway carrying this call drops a round-trip at sixty seconds,
	// taking the member's terminal connection with it.
	if githubProbeTimeout >= 60*time.Second {
		t.Fatalf("githubProbeTimeout = %v, want it well inside the gateway's sixty seconds", githubProbeTimeout)
	}
	restore := githubProbeTimeout
	githubProbeTimeout = 20 * time.Millisecond
	t.Cleanup(func() { githubProbeTimeout = restore })

	start := time.Now()
	if _, err := e.sched.ProbeGitHubCLI(t.Context(), e.member.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ProbeGitHubCLI error = %v, want the deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("ProbeGitHubCLI returned after %v, want it to give up at its deadline", elapsed)
	}
}

// Without a terminal there is no container to ask, and opening one can take
// an image pull the gateway carrying this call will not wait for.
func TestProbeGitHubCLINeedsARunningTerminal(t *testing.T) {
	e := newTestEnv(t, nil)
	_, err := e.sched.ProbeGitHubCLI(t.Context(), e.member.ID)
	if !errors.Is(err, ErrTerminalNotRunning) {
		t.Fatalf("ProbeGitHubCLI error = %v, want %v", err, ErrTerminalNotRunning)
	}
}

// gh marks one account active when a host holds several; the fallback is
// for the single-account answer, and must not turn a failed account into a
// login.
func TestActiveGitHubLogin(t *testing.T) {
	for _, tc := range []struct {
		name      string
		stdout    string
		wantLogin string
		wantOK    bool
	}{
		{
			name:      "one account, no active marker",
			stdout:    `{"hosts":{"github.com":[{"state":"success","login":"octocat"}]}}`,
			wantLogin: "octocat", wantOK: true,
		},
		{
			name:   "one failed account, no active marker",
			stdout: `{"hosts":{"github.com":[{"state":"error","login":"octocat","error":"401"}]}}`,
			// The entry comes back for the error it carries.
			wantLogin: "octocat", wantOK: false,
		},
		{
			name:      "two accounts, one active",
			stdout:    `{"hosts":{"github.com":[{"state":"success","login":"other"},{"state":"success","active":true,"login":"octocat"}]}}`,
			wantLogin: "octocat", wantOK: true,
		},
		{
			name:   "two accounts, none active",
			stdout: `{"hosts":{"github.com":[{"state":"success","login":"one"},{"state":"success","login":"two"}]}}`,
			wantOK: false,
		},
		{name: "no accounts", stdout: `{"hosts":{}}`, wantOK: false},
		{name: "not json", stdout: "unknown flag: --json", wantOK: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry, ok := activeGitHubLogin(tc.stdout)
			if ok != tc.wantOK || entry.Login != tc.wantLogin {
				t.Errorf("activeGitHubLogin = (%+v, %v), want login %q ok %v", entry, ok, tc.wantLogin, tc.wantOK)
			}
		})
	}
}

// Raising the minimum above the gh the standard image ships would tell
// every member on that image that their environment is broken.
func TestStandardImageShipsAUsableGh(t *testing.T) {
	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "images", "standard", "Dockerfile"))
	if err != nil {
		t.Fatalf("read the standard image Dockerfile: %v", err)
	}
	pin := regexp.MustCompile(`ARG GH_VERSION=(\d+\.\d+\.\d+)`).FindSubmatch(dockerfile)
	if pin == nil {
		t.Fatal("the standard image Dockerfile no longer pins GH_VERSION")
	}
	shipped, ok := parseGitHubCLIVersion("gh version " + string(pin[1]))
	if !ok {
		t.Fatalf("cannot read the pinned gh version %q", pin[1])
	}
	if slices.Compare(shipped, minGitHubCLI) < 0 {
		t.Errorf("the standard image ships gh %s, older than the %s the login check needs",
			pin[1], versionString(minGitHubCLI))
	}
}
