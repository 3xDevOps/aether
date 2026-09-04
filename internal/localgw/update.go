package localgw

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/localops"
	"github.com/3xDevOps/Aether/internal/macinstall"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
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
// A server that does not answer costs the server half only. The CLI half
// is about a binary on this machine and has nothing to do with the SSH
// hop, and failing the whole verb would take the CLI banner down with it
// during exactly the outage the user is most likely to be looking at.
func (g *Gateway) localUpdateCheck(r *http.Request, _ []byte) (any, *protocol.Error) {
	check, perr := g.checkRelease(r)
	if perr != nil {
		return nil, perr
	}
	out := struct {
		CLI           selfupdate.Check `json:"cli"`
		ServerVersion string           `json:"server_version"`
		ServerBehind  bool             `json:"server_behind"`
		ServerError   string           `json:"server_error,omitempty"`
		Supervised    bool             `json:"supervised"`
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
	}{CLI: check, Supervised: g.cfg.Supervised, ShellBuildError: localops.LastDesktopBuildError()}
	if access, err := probeAccess(); err == nil {
		out.CLIPath, out.InstallMethod = access.Path, access.Method
	}

	result, perr := g.cfg.Backend.Call(r.Context(), protocol.MethodServerInfo, nil)
	if perr != nil {
		out.ServerError = perr.Message
		return out, nil
	}
	var info protocol.ServerInfoResult
	if err := json.Unmarshal(result, &info); err != nil {
		out.ServerError = "decode server info: " + err.Error()
		return out, nil
	}
	out.ServerVersion = info.ServerVersion
	out.ServerBehind = selfupdate.Behind(info.ServerVersion, check.Latest)
	return out, nil
}

// localUpdateApply installs the newest release over this CLI. This
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
	check, perr := g.checkRelease(r)
	if perr != nil {
		return nil, perr
	}
	// One install at a time: a second click from another tab while the
	// dialog is up would stack a second dialog and a second copy.
	if !g.updating.CompareAndSwap(false, true) {
		return nil, &protocol.Error{Code: protocol.CodeConflict,
			Message: "an update is already running in this gateway"}
	}
	defer g.updating.Store(false)
	switch {
	case check.Dev:
		return nil, &protocol.Error{Code: protocol.CodeInvalidState,
			Message: "this is a dev build with no release to update to; rebuild it with `make build`"}
	case check.Disabled:
		return nil, &protocol.Error{Code: protocol.CodeInvalidState,
			Message: "release checks are off: " + selfupdate.OptOutEnv + " is set in this process's environment"}
	case !check.CanSelfUpdate:
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: selfupdate.ErrWindows.Error()}
	case !check.UpdateAvailable:
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
	if done := g.installed.Load(); done != nil && done.tag == check.Latest {
		updated = done.paths
	} else {
		var err error
		updated, err = applyUpdate(r.Context(), g.cfg.Update.BaseURL(), check.Latest)
		if err != nil {
			return nil, applyError(err, check.Latest)
		}
		g.installed.Store(&installedRelease{tag: check.Latest, paths: updated})
	}

	// The CLI is swapped; the Electron shell around the dashboard is not,
	// and it is built from the sources inside the binary that just landed.
	// updated[0] is that binary, symlinks already resolved. A rebuild this
	// process already finished is not run again for a repeat click: the
	// app on disk is new, and only the restart is left.
	outcome := rebuildNone
	rebuilt := g.rebuild.snapshot().Phase == localops.PhaseDone
	if len(updated) > 0 && !rebuilt {
		outcome = g.startAppRebuild(updated[0])
	}
	rebuilding := outcome != rebuildNone

	note := "rerun aether gui to use the new binary"
	switch {
	case rebuilt:
		note = "the desktop app was rebuilt; restart it to use the new version"
	case outcome == rebuildBusy:
		// A second apply - another tab, or the app beside a browser tab.
		// Saying a rebuild is running is the honest answer, and it must
		// not exit: the first build is still swapping the app directory.
		note = "a rebuild of the desktop app is already running"
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
		Updated: updated, Version: check.Latest, Restarting: g.cfg.Supervised,
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

// checkRelease runs the cached release check, mapping an unreachable
// GitHub to the same code every other unavailable dependency uses.
func (g *Gateway) checkRelease(r *http.Request) (selfupdate.Check, *protocol.Error) {
	check, err := g.cfg.Update.Check(r.Context())
	if err != nil {
		return check, &protocol.Error{Code: protocol.CodeUnavailable, Message: "check for releases: " + err.Error()}
	}
	return check, nil
}
