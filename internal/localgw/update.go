package localgw

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
)

// applyUpdate is selfupdate.Update behind a variable because it replaces
// the running executable, which a test process must never do to itself.
var applyUpdate = selfupdate.Update

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
	}{CLI: check, Supervised: g.cfg.Supervised}

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

// localUpdateApply installs the newest release over this CLI. It never
// escalates privileges: a binary the user cannot write is a refusal that
// names the command to run in a terminal instead.
func (g *Gateway) localUpdateApply(r *http.Request, _ []byte) (any, *protocol.Error) {
	check, perr := g.checkRelease(r)
	if perr != nil {
		return nil, perr
	}
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

	updated, err := applyUpdate(r.Context(), g.cfg.Update.BaseURL(), check.Latest)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, &protocol.Error{Code: protocol.CodeInvalidState,
				Message: err.Error() + " (the dashboard never escalates privileges)"}
		}
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "install " + check.Latest + ": " + err.Error()}
	}

	note := "rerun aether gui to use the new binary"
	if g.cfg.Supervised {
		note = "the desktop app restarts with the new binary"
		// Close after the handler returns and Shutdown drains it: the
		// command waiting on Exit calls Close, which lets this response
		// finish writing before the process goes away.
		g.requestExit()
	}
	return struct {
		Updated        []string `json:"updated"`
		Version        string   `json:"version"`
		Restarting     bool     `json:"restarting"`
		Note           string   `json:"note,omitempty"`
		RestartCommand string   `json:"restart_command,omitempty"`
	}{
		Updated: updated, Version: check.Latest, Restarting: g.cfg.Supervised, Note: note,
		RestartCommand: restartCommand(updated),
	}, nil
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
