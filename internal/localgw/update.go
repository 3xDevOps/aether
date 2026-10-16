package localgw

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/localops"
	"github.com/3xDevOps/Aether/internal/macinstall"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/version"
)

// applyUpdate is selfupdate.UpdateWithAuthorization behind a variable
// because it replaces the running executable, which a test process must
// never do to itself. The authorized variant because a dashboard click is
// a GUI action: on macOS a binary this user cannot write goes through the
// system's administrator dialog instead of a refusal.
var applyUpdate = selfupdate.UpdateWithAuthorization

// probeAccess is selfupdate.Probe behind a variable so tests can describe
// a binary that is not the test process.
var probeAccess = selfupdate.Probe

// localUpdateCheck reports the CLI's release check beside the linked
// server's version, so the dashboard can tell the user which half of the
// pair is behind.
//
// Each half fails alone. A server that does not answer costs the server
// half only: the CLI half is about a binary on this machine, and failing
// the whole verb would take the CLI banner down with it during exactly the
// outage the user is most likely to be looking at. A release lookup that
// cannot reach GitHub costs the CLI half only, so a server that refuses
// this client still says so.
func (g *Gateway) localUpdateCheck(r *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		Refresh bool `json:"refresh"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	check, perr := g.checkRelease(r, params.Refresh)
	out := struct {
		CLI selfupdate.Check `json:"cli"`
		// CLIError is why the release lookup failed; cli then carries
		// only this build's own fields.
		CLIError      string `json:"cli_error,omitempty"`
		ServerVersion string `json:"server_version"`
		ServerBehind  bool   `json:"server_behind"`
		ServerError   string `json:"server_error,omitempty"`
		// ClientUpdateRequired reports that the server refuses this CLI
		// until it updates to MinClientVersion, which server.info names.
		ClientUpdateRequired bool   `json:"client_update_required"`
		MinClientVersion     string `json:"min_client_version"`
		Supervised           bool   `json:"supervised"`
		// ShellBuildError is why the last desktop-app rebuild failed. The
		// gateway that ran it exited so the shell could respawn on the new
		// CLI, so this gateway reads the record it left behind: without it
		// the app would just look stale, with no sign of what went wrong.
		ShellBuildError string `json:"shell_build_error,omitempty"`
		// CLIPath is the binary update.apply replaces, symlinks resolved,
		// and InstallMethod how: "direct" with no questions asked,
		// "admin-prompt" through the macOS administrator dialog, or
		// "manual" with the command the banner shows. Both are empty when
		// the probe itself failed; the click then reports that error.
		CLIPath       string            `json:"cli_path,omitempty"`
		InstallMethod selfupdate.Method `json:"install_method,omitempty"`
		// TargetVersion is the release update.apply would install, empty
		// when it would install nothing.
		TargetVersion string `json:"target_version"`
	}{CLI: check, Supervised: g.cfg.Supervised, ShellBuildError: localops.LastDesktopBuildError()}
	if perr != nil {
		out.CLIError = perr.Message
	}
	if access, err := probeAccess(); err == nil {
		out.CLIPath, out.InstallMethod = access.Path, access.Method
	}

	info, err := g.serverInfo(r)
	out.TargetVersion = updateTarget(check, info.ServerVersion)
	if err != nil {
		out.ServerError = err.Error()
		return out, nil
	}
	out.ServerVersion = info.ServerVersion
	out.ServerBehind = selfupdate.Behind(info.ServerVersion, check.Latest)
	out.MinClientVersion = info.MinClientVersion
	out.ClientUpdateRequired = cli.UpgradeRequired(info) != nil
	return out, nil
}

func (g *Gateway) serverInfo(r *http.Request) (protocol.ServerInfoResult, error) {
	var info protocol.ServerInfoResult
	result, perr := g.cfg.Backend.Call(r.Context(), protocol.MethodServerInfo, nil)
	if perr != nil {
		return info, errors.New(perr.Message)
	}
	if err := json.Unmarshal(result, &info); err != nil {
		return info, fmt.Errorf("decode server info: %w", err)
	}
	return info, nil
}

// updateTarget is the release update.apply installs. A linked server
// ahead of this CLI decides it: the client matches its server rather than
// whatever GitHub calls latest, which skips prereleases and can lag a
// publish. Otherwise it is the newest release when that is newer. The
// server's version is checked as a tag because it lands in a download URL.
func updateTarget(check selfupdate.Check, serverVersion string) string {
	if check.Dev || check.Disabled || !check.CanSelfUpdate {
		return ""
	}
	if core := version.ReleaseCore(serverVersion); selfupdate.ValidTag(core) && selfupdate.Behind(check.Version, core) {
		return core
	}
	if check.UpdateAvailable {
		return check.Latest
	}
	return ""
}

// localUpdateApply installs updateTarget's release over this CLI. This
// process never gains privileges: on macOS a binary the user cannot write
// is installed by the system's administrator dialog, one fixed command
// run by root outside this process; elsewhere it is a refusal that names
// the command to run in a terminal instead.
//
// The call waits on the request context alone while the dialog is up.
// There is no timer to add here, and the http.Server in localgw.go must
// keep having no WriteTimeout: a native authorization dialog has no
// timeout, and one that closes the request under the user would leave the
// dialog on screen with the password authorizing nothing. Closing the tab
// or the app cancels the request, which kills the wait.
func (g *Gateway) localUpdateApply(r *http.Request, _ []byte) (any, *protocol.Error) {
	// Refused from the config, before any lookup: a packaged binary is
	// replaced only by its app, whether or not GitHub answers.
	if app := g.cfg.Packaged; app != "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidState,
			Message: "this aether is part of the " + app + ", which installs its updates; update the " + app + " instead"}
	}
	// One install at a time: a second click from another tab while the
	// dialog is up would stack a second dialog and a second copy. Claimed
	// before the release lookup, so the loser does not pay for a dial.
	if !g.updating.CompareAndSwap(false, true) {
		return nil, &protocol.Error{Code: protocol.CodeConflict,
			Message: "a release lookup or install is already running in this gateway"}
	}
	defer g.updating.Store(false)
	check, lookupErr := g.checkRelease(r, true)
	info, _ := g.serverInfo(r)
	target := updateTarget(check, info.ServerVersion)
	switch {
	case lookupErr != nil && target == "":
		return nil, lookupErr
	case check.Dev:
		return nil, &protocol.Error{Code: protocol.CodeInvalidState,
			Message: "this is a dev build with no release to update to; rebuild it with `make build`"}
	case check.Disabled:
		return nil, &protocol.Error{Code: protocol.CodeInvalidState,
			Message: "release checks are off: " + selfupdate.OptOutEnv + " is set in this process's environment"}
	case !check.CanSelfUpdate:
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: selfupdate.ErrWindows.Error()}
	case target == "" && check.Latest == "":
		return nil, &protocol.Error{Code: protocol.CodeInvalidState,
			Message: "no signed release is published yet; this aether stays on " + check.Version}
	case target == "":
		// Nothing to install. Downloading the running version over itself
		// would answer success for work that changed nothing, and on a
		// supervised gateway it would restart the app for no reason.
		return nil, &protocol.Error{Code: protocol.CodeInvalidState,
			Message: "already on " + check.Latest + ", the newest release"}
	}

	// A release this process already installed is on disk; only the
	// rebuild or the restart is still to happen. Swapping it again would
	// cost a download and, on macOS, a second password dialog for the
	// same bytes, so the answer picks up after the swap instead.
	var updated []string
	if done := g.installed.Load(); done != nil && done.tag == target {
		updated = done.paths
	} else {
		var err error
		updated, err = applyUpdate(r.Context(), g.cfg.Update.BaseURL(), target)
		if err != nil {
			return nil, applyError(err, target)
		}
		g.installed.Store(&installedRelease{tag: target, paths: updated})
	}

	// The CLI is swapped; the Electron shell around the dashboard is not,
	// and it is built from the sources inside the binary that just landed.
	// updated[0] is that binary, symlinks already resolved. A rebuild this
	// process already finished is not run again for a repeat click: the
	// app on disk is new, and only the restart is left.
	outcome := rebuildNone
	rebuilt := g.rebuild.builtRelease() == target
	if len(updated) > 0 && !rebuilt {
		outcome = g.startAppRebuild(updated[0], target)
	}
	// A build for an earlier release covers nothing here: reporting it as
	// this release's rebuild would have the dashboard watch it finish and
	// then say the app was rebuilt, when it was built from the tag before.
	rebuilding := outcome == rebuildStarted || outcome == rebuildBusy

	note := "rerun aether gui to use the new binary"
	switch {
	case rebuilt:
		note = "the desktop app was rebuilt; restart it to use the new version"
	case outcome == rebuildBusy:
		// A second apply - another tab, or the app beside a browser tab.
		// Saying a rebuild is running is the honest answer, and it must
		// not exit: the first build is still swapping the app directory.
		note = "a rebuild of the desktop app is already running"
	case outcome == rebuildStale:
		note = "a rebuild of an earlier release is still running; rerun the update once it finishes"
	case rebuilding && g.cfg.Supervised:
		note = "rebuilding the desktop app, then relaunching it"
	case rebuilding:
		// A gateway nobody supervises must not exit under the user, so the
		// browser tab keeps working and the user restarts the app itself.
		note = "rebuilding the desktop app; restart it when the rebuild finishes"
	case g.cfg.Supervised:
		note = "the desktop app restarts with the new binary"
	}
	if g.cfg.Supervised && outcome == rebuildNone && !rebuilt {
		// Close after the handler returns and Shutdown drains it: the
		// command waiting on Exit calls Close, which lets this response
		// finish writing before the process goes away. With a rebuild
		// running - this call's or an earlier one's - the exit waits for
		// it, so the shell relaunches onto the new app rather than the old
		// one, and never mid-swap.
		g.requestExit(0)
	}
	return struct {
		Updated        []string `json:"updated"`
		Version        string   `json:"version"`
		Restarting     bool     `json:"restarting"`
		Rebuilding     bool     `json:"rebuilding"`
		Note           string   `json:"note,omitempty"`
		RestartCommand string   `json:"restart_command,omitempty"`
	}{
		Updated: updated, Version: target, Restarting: g.cfg.Supervised,
		Rebuilding: rebuilding, Note: note,
		RestartCommand: restartCommand(updated),
	}, nil
}

// applyError maps what the install returned to the answer the banner
// acts on. Each carries the underlying message verbatim: osascript's own
// line for the dialog, CheckWritable's sudo command for a refusal.
func applyError(err error, latest string) *protocol.Error {
	switch {
	case errors.Is(err, macinstall.ErrCanceled):
		// The user dismissed the dialog, or macOS gave up on the password.
		// Not a failure of the install: nothing was downloaded over
		// anything, and the button is worth another click.
		return &protocol.Error{Code: protocol.CodeDenied,
			Message: "nothing was changed: " + err.Error()}
	case errors.Is(err, macinstall.ErrNoSession):
		// A gateway with no window server - started over SSH - has no way
		// to show the dialog; the terminal it was started from does have
		// sudo.
		return &protocol.Error{Code: protocol.CodeInvalidState,
			Message: err.Error() + "; run `sudo aether update` in a terminal on this Mac"}
	case errors.Is(err, os.ErrPermission):
		// Linux: the message already ends with the sudo command.
		return &protocol.Error{Code: protocol.CodeInvalidState, Message: err.Error()}
	}
	return &protocol.Error{Code: protocol.CodeUnavailable, Message: "install " + latest + ": " + err.Error()}
}

// localUpdateStatus reports the desktop-app rebuild update.apply started,
// which the dashboard polls once a second while its banner shows progress.
// It answers "idle" when no rebuild has run in this process, including in
// the gateway that comes up after one.
func (g *Gateway) localUpdateStatus(_ *http.Request, _ []byte) (any, *protocol.Error) {
	return g.rebuild.snapshot(), nil
}

// restartCommand names the follow-up a swapped aether-server needs, the
// same one `aether update` prints. selfupdate.Update replaces the server
// binary beside the CLI on a single-box install, and the running server
// keeps the old code until someone restarts the unit - so the answer has
// to carry the command rather than leave the operator to notice.
func restartCommand(updated []string) string {
	for _, path := range updated {
		if filepath.Base(path) == "aether-server" {
			return "sudo systemctl restart aether-server"
		}
	}
	return ""
}

// checkRelease runs the release check, mapping an unreachable GitHub to
// the same code every other unavailable dependency uses.
func (g *Gateway) checkRelease(r *http.Request, fresh bool) (selfupdate.Check, *protocol.Error) {
	resolve := g.cfg.Update.Check
	if fresh {
		resolve = g.cfg.Update.CheckFresh
	}
	check, err := resolve(r.Context())
	if err != nil {
		return check, &protocol.Error{Code: protocol.CodeUnavailable, Message: "check for releases: " + err.Error()}
	}
	return check, nil
}
