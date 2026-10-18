//go:build !windows

// The apply path on Unix: these tests stub the rebuild with a shell script
// and use Unix paths. Windows is covered in update_apply_windows_test.go.

package localgw

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/localops"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/version"
)

func TestUpdateApplyCarriesTheServerRestart(t *testing.T) {
	pinVersion(t)
	// The rebuild path is stubbed to "no app installed" in every apply
	// test that is not about the rebuild: with a real desktop app on the
	// developer's machine, update.apply would otherwise spawn a genuine
	// `aether gui build` and flip the response to its rebuilding shape.
	stubRebuild(t, "/bin/true", false)
	g := updateGateway(t, &verbStubBackend{}, false)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		// A single-box install: the swap replaced the server binary too.
		return []string{"/usr/local/bin/aether", "/usr/local/bin/aether-server"}, nil
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Updated        []string `json:"updated"`
		RestartCommand string   `json:"restart_command"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Updated) != 2 {
		t.Fatalf("updated = %v, want both binaries", got.Updated)
	}
	if got.RestartCommand != "sudo systemctl restart aether-server" {
		t.Fatalf("restart_command = %q, want the command aether update prints", got.RestartCommand)
	}
}

func TestUpdateApplyOmitsTheRestartWithoutAServer(t *testing.T) {
	pinVersion(t)
	stubRebuild(t, "/bin/true", false)
	g := updateGateway(t, &verbStubBackend{}, false)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		return []string{"/usr/local/bin/aether"}, nil
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	// Assert the status before the body: an error envelope decodes into
	// this struct just as happily, leaving restart_command empty, so a
	// refusal would otherwise satisfy the assertion below.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		RestartCommand string `json:"restart_command"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.RestartCommand != "" {
		t.Fatalf("restart_command = %q on a client-only machine", got.RestartCommand)
	}
}

func TestUpdateApplyRefusesWhenAlreadyCurrent(t *testing.T) {
	old := version.Version
	t.Cleanup(func() { version.Version = old })
	version.Version = releaseTag
	g := updateGateway(t, &verbStubBackend{}, true)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		t.Fatal("the newest release must not be downloaded over itself")
		return nil, nil
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeInvalidState)
	}
	if !strings.Contains(perr.Message, releaseTag) {
		t.Fatalf("message = %q, want it to name the release already installed", perr.Message)
	}
	select {
	case <-g.Exit():
		t.Fatal("a refused update asked the process to exit")
	default:
	}
}

func TestUpdateApplyRefusesUnwritableBinary(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, true)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		return nil, fmt.Errorf("/usr/local/bin is not writable by this user, re-run as: sudo aether update: %w", os.ErrPermission)
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeInvalidState)
	}
	if !strings.Contains(perr.Message, "sudo aether update") {
		t.Fatalf("message = %q, want it to name sudo aether update", perr.Message)
	}
	select {
	case <-g.Exit():
		t.Fatal("a refused update asked the process to exit")
	default:
	}
}

func TestUpdateApplySupervisedExits(t *testing.T) {
	pinVersion(t)
	stubRebuild(t, "/bin/true", false)
	g := updateGateway(t, &verbStubBackend{}, true)
	stubApply(t, func(_ context.Context, baseURL, tag string) ([]string, error) {
		if tag != releaseTag {
			t.Errorf("tag = %q, want %s", tag, releaseTag)
		}
		if baseURL != g.cfg.Update.BaseURL() {
			t.Errorf("baseURL = %q, want the checker's %q", baseURL, g.cfg.Update.BaseURL())
		}
		return []string{"/usr/local/bin/aether", "/usr/local/bin/aether-server"}, nil
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Updated    []string `json:"updated"`
		Version    string   `json:"version"`
		Restarting bool     `json:"restarting"`
		Note       string   `json:"note"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Updated) != 2 || got.Version != releaseTag || !got.Restarting {
		t.Fatalf("apply = %+v", got)
	}
	select {
	case <-g.Exit():
	default:
		t.Fatal("a supervised apply left the exit channel open")
	}
}

func TestUpdateApplyUnsupervisedKeepsServing(t *testing.T) {
	pinVersion(t)
	stubRebuild(t, "/bin/true", false)
	g := updateGateway(t, &verbStubBackend{}, false)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		return []string{"/usr/local/bin/aether"}, nil
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Restarting bool   `json:"restarting"`
		Note       string `json:"note"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Restarting {
		t.Error("restarting = true without a supervising shell")
	}
	if !strings.Contains(got.Note, "aether gui") {
		t.Errorf("note = %q, want it to name aether gui", got.Note)
	}
	select {
	case <-g.Exit():
		t.Fatal("an unsupervised apply asked the process to exit")
	default:
	}
}

// TestUpdateApplySupervisedResponseSurvivesShutdown drives the real
// listener the way the desktop shell does: the process waits on Exit and
// closes the gateway the moment update.apply asks it to. The client must
// still receive the whole response, which is what Close's Shutdown drain
// buys.
func TestUpdateApplySupervisedResponseSurvivesShutdown(t *testing.T) {
	pinVersion(t)
	stubRebuild(t, "/bin/true", false)
	g := updateGateway(t, &verbStubBackend{}, true)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		return []string{"/usr/local/bin/aether"}, nil
	})
	if err := g.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	closed := make(chan error, 1)
	go func() {
		<-g.Exit()
		closed <- g.Close()
	}()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		"http://"+g.Addr()+"/local/v1/update.apply", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+g.Token())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got struct {
		Updated    []string `json:"updated"`
		Restarting bool     `json:"restarting"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response after shutdown: %v", err)
	}
	if len(got.Updated) != 1 || !got.Restarting {
		t.Fatalf("apply = %+v", got)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(closeTimeout + time.Second):
		t.Fatal("gateway never shut down")
	}
}

// The dashboard checks, a release ships while the app is open, and the
// member clicks Update. The click must install what is newest now, not the
// tag the cached check answered with.
func TestUpdateApplyInstallsTheReleaseThatIsNewestAtTheClick(t *testing.T) {
	pinVersion(t)
	stubRebuild(t, "/bin/true", false)
	g, setTag := releasingGateway(t, &verbStubBackend{}, false)

	rec := do(g, http.MethodPost, "/local/v1/update.check", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("check status = %d: %s", rec.Code, rec.Body)
	}

	setTag("v1.4.0")
	var installed string
	stubApply(t, func(_ context.Context, _, tag string) ([]string, error) {
		installed = tag
		return []string{"/usr/local/bin/aether"}, nil
	})

	rec = do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("apply status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if installed != "v1.4.0" {
		t.Errorf("installed %q, want the release that is newest at the click", installed)
	}
	if got.Version != "v1.4.0" {
		t.Errorf("version = %q, want the tag that was installed", got.Version)
	}
}

// A release this process already installed is on disk, but a newer one is
// not: the guard that skips the second download keys on the release, not on
// "something was installed here".
func TestUpdateApplyInstallsAReleaseNewerThanTheOneOnDisk(t *testing.T) {
	pinVersion(t)
	stubRebuild(t, "/bin/true", false)
	g, setTag := releasingGateway(t, &verbStubBackend{}, false)

	var installed []string
	stubApply(t, func(_ context.Context, _, tag string) ([]string, error) {
		installed = append(installed, tag)
		return []string{"/usr/local/bin/aether"}, nil
	})
	for range 2 {
		if rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true); rec.Code != http.StatusOK {
			t.Fatalf("apply status = %d: %s", rec.Code, rec.Body)
		}
	}
	setTag("v1.4.0")
	if rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true); rec.Code != http.StatusOK {
		t.Fatalf("apply status = %d: %s", rec.Code, rec.Body)
	}
	if want := []string{releaseTag, "v1.4.0"}; !slices.Equal(installed, want) {
		t.Fatalf("installed %v, want %v: one download per release", installed, want)
	}
}

// A release shipping mid-build: the running build is for the release
// before this one, so it covers nothing here and must not be reported as
// this release's rebuild.
func TestUpdateApplyDoesNotClaimAnEarlierReleasesRebuild(t *testing.T) {
	pinVersion(t)
	stubRebuild(t, scriptBuild(t, `printf '{"phase":"packaging"}\n'`+"\nexec sleep 120\n"), true)
	g, setTag := releasingGateway(t, &verbStubBackend{}, false)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		return []string{"/usr/local/bin/aether"}, nil
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("first apply status = %d: %s", rec.Code, rec.Body)
	}
	awaitPhase(t, g, localops.PhasePackaging)
	t.Cleanup(func() { _ = g.Close() })

	setTag("v1.4.0")
	rec = do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("second apply status = %d: %s", rec.Code, rec.Body)
	}
	var got applyAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != "v1.4.0" {
		t.Fatalf("version = %q, want the newer release", got.Version)
	}
	if got.Rebuilding {
		t.Fatalf("apply = %+v, want no claim on a build made from %s", got, releaseTag)
	}
	if !strings.Contains(got.Note, "earlier release") {
		t.Fatalf("note = %q, want it to name the build that is running", got.Note)
	}
}

// The banner offered an update from a cached check and the fresh resolve
// disagrees - the release was yanked, or `aether update` ran in a terminal
// while the app was open. Nothing may be downloaded over a current build.
func TestUpdateApplyRefusesWhenTheFreshResolveSaysItIsCurrent(t *testing.T) {
	pinVersion(t)
	stubRebuild(t, "/bin/true", false)
	g, setTag := releasingGateway(t, &verbStubBackend{}, false)

	if rec := do(g, http.MethodPost, "/local/v1/update.check", "{}", true); rec.Code != http.StatusOK {
		t.Fatalf("check status = %d: %s", rec.Code, rec.Body)
	}
	setTag(runningTag)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		t.Fatal("this build is already on the newest release; nothing may be downloaded")
		return nil, nil
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeInvalidState)
	}
	if !strings.Contains(perr.Message, "already on "+runningTag) {
		t.Fatalf("message = %q, want the newest-release refusal", perr.Message)
	}
}

// A server that refuses this client names the release to match. GitHub's
// latest skips prereleases and can lag a publish, so the server's own
// release is the target whenever it is ahead; otherwise nothing changes.
func TestUpdateApplyTargetsTheLinkedServerRelease(t *testing.T) {
	cases := []struct {
		name, server, latest, want string
	}{
		{"server ahead of an older latest", "v1.3.1-alpha.2-4-gabc1234", runningTag, "v1.3.1-alpha.2"},
		{"server equal", runningTag, releaseTag, releaseTag},
		{"server behind", "v1.2.8", releaseTag, releaseTag},
		{"server behind and nothing newer", "v1.2.8", runningTag, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pinVersion(t)
			stubRebuild(t, "/bin/true", false)
			backend := &verbStubBackend{apiStubBackend: apiStubBackend{results: map[string]json.RawMessage{
				protocol.MethodServerInfo: json.RawMessage(`{"server_version":"` + tc.server + `"}`),
			}}}
			g, setTag := releasingGateway(t, backend, false)
			setTag(tc.latest)
			var installed string
			stubApply(t, func(_ context.Context, _, tag string) ([]string, error) {
				installed = tag
				return []string{"/usr/local/bin/aether"}, nil
			})

			rec := do(g, http.MethodPost, "/local/v1/update.check", `{"refresh":true}`, true)
			var check struct {
				TargetVersion string `json:"target_version"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &check); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("check status = %d: %s", rec.Code, rec.Body)
			}
			if check.TargetVersion != tc.want {
				t.Fatalf("target_version = %q, want %q", check.TargetVersion, tc.want)
			}

			rec = do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
			if tc.want == "" {
				perr := decodeError(t, rec.Body.Bytes())
				if !strings.Contains(perr.Message, "already on "+runningTag) || installed != "" {
					t.Fatalf("message = %q, installed %q; want the newest-release refusal", perr.Message, installed)
				}
				return
			}
			var got struct {
				Version string `json:"version"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("apply status = %d: %s", rec.Code, rec.Body)
			}
			if installed != tc.want || got.Version != tc.want {
				t.Fatalf("installed %q, answered %q; want %q", installed, got.Version, tc.want)
			}
		})
	}
}

// An admin whose server can update itself moves the server first, as
// `aether update` requires: update.check names no target and says why,
// and update.apply refuses. Without the server's own update the admin
// installs, as every other member does.
func TestUpdateApplyLeavesTheServerFirst(t *testing.T) {
	for _, capable := range []bool{true, false} {
		t.Run(fmt.Sprintf("capable=%v", capable), func(t *testing.T) {
			pinVersion(t)
			stubRebuild(t, "/bin/true", false)
			backend := &verbStubBackend{apiStubBackend: apiStubBackend{results: map[string]json.RawMessage{
				protocol.MethodServerInfo:         json.RawMessage(`{"server_version":"v1.2.8","member":{"role":"admin"}}`),
				protocol.MethodServerUpdateStatus: json.RawMessage(fmt.Sprintf(`{"capable":%v}`, capable)),
			}}}
			g := updateGateway(t, backend, false)
			var installed string
			stubApply(t, func(_ context.Context, _, tag string) ([]string, error) {
				installed = tag
				return []string{"/usr/local/bin/aether"}, nil
			})

			rec := do(g, http.MethodPost, "/local/v1/update.check", "{}", true)
			var check struct {
				TargetVersion string `json:"target_version"`
				ServerFirst   bool   `json:"server_first"`
				CanSelfUpdate bool   `json:"can_self_update"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &check); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("check status = %d: %s", rec.Code, rec.Body)
			}
			if !check.CanSelfUpdate {
				t.Error("can_self_update = false on a binary update.apply can replace")
			}
			if check.ServerFirst != capable || (check.TargetVersion == "") != capable {
				t.Fatalf("server_first = %v target_version = %q with capable=%v", check.ServerFirst, check.TargetVersion, capable)
			}

			rec = do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
			if !capable {
				if rec.Code != http.StatusOK || installed != releaseTag {
					t.Fatalf("apply status = %d, installed %q: %s", rec.Code, installed, rec.Body)
				}
				return
			}
			perr := decodeError(t, rec.Body.Bytes())
			if perr.Code != protocol.CodeInvalidState || !strings.HasPrefix(perr.Message, "update the server first: aether server update") || installed != "" {
				t.Fatalf("error = %d %q, installed %q; want the server-first refusal", perr.Code, perr.Message, installed)
			}
		})
	}
}
