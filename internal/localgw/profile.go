package localgw

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/3xDevOps/Aether/internal/cli/profile"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// localProfilePreview reports what a push of one harness profile would
// carry from this machine, and what the guards would leave behind.
// Nothing is uploaded and the linked server is never called: the walk,
// the denylist, the ignore file, and the scanner all run here.
func (g *Gateway) localProfilePreview(r *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		Harness string `json:"harness"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	if params.Harness == "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "harness is required"}
	}
	// LocalDir runs first so an unknown harness, or one with no profile
	// sync, is a params error in the caller's own words. Everything
	// Inventory can still fail on afterwards is this machine's
	// filesystem; a harness root that is simply absent is not a failure
	// at all, and comes back as present:false.
	if _, _, err := profile.LocalDir(params.Harness); err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()}
	}
	// The request context stops the walk when the browser gives up or
	// navigates away: a profile root can hold thousands of files, and
	// nobody is waiting for the answer any more.
	preview, err := profile.Inventory(r.Context(), params.Harness)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	return preview, nil
}

// localProfilePush uploads one harness profile from this machine as a
// content-addressed delta against the server's current head.
//
// There is no allow_secret parameter and the gateway never builds one: a
// scanner finding refuses the push here and names the CLI command that
// can override it, where --workspace makes the override attributable to
// a workspace timeline entry.
func (g *Gateway) localProfilePush(r *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		Harness string `json:"harness"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	if params.Harness == "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "harness is required"}
	}
	root, _, err := profile.LocalDir(params.Harness)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()}
	}
	files, skipped, err := profile.DiscoverFiles(r.Context(), params.Harness, nil)
	if err != nil {
		// The harness name is already valid, so everything Discover
		// refuses - a finding, a symlink escape, a root that is not
		// there - is the state of the user's own profile directory.
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: pushRefusal(root, params.Harness, err)}
	}
	pushParams, err := json.Marshal(profile.PushParams(g.knownDigests(r, params.Harness), params.Harness, files, nil, ""))
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	result, perr := g.cfg.Backend.Call(r.Context(), protocol.MethodProfilePush, pushParams)
	if perr != nil {
		return nil, perr
	}
	var pushed protocol.ProfilePushResult
	if err := json.Unmarshal(result, &pushed); err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "decode profile push result: " + err.Error()}
	}
	var totalBytes int64
	for _, f := range files {
		totalBytes += int64(len(f.Content))
	}
	return struct {
		Harness    string              `json:"harness"`
		SnapshotID string              `json:"snapshot_id"`
		Digest     string              `json:"digest"`
		Files      int                 `json:"files"`
		Bytes      int64               `json:"bytes"`
		Skipped    []profile.Exclusion `json:"skipped,omitempty"`
	}{
		Harness:    params.Harness,
		SnapshotID: pushed.Snapshot.ID,
		Digest:     pushed.Snapshot.Digest,
		Files:      len(files),
		Bytes:      totalBytes,
		// Files the size caps left behind. The push succeeded without
		// them, so this is the only place the user learns they are not
		// on the server.
		Skipped: skipped,
	}, nil
}

// knownDigests is the set of blob digests the server already holds for a
// harness, so the push carries only what is new. A status failure is not
// fatal: an empty set pushes every blob, which is what the CLI's own
// BuildPushParams falls back to.
func (g *Gateway) knownDigests(r *http.Request, harness string) map[string]struct{} {
	statusParams, err := json.Marshal(protocol.ProfileStatusParams{Harness: harness})
	if err != nil {
		return nil
	}
	result, perr := g.cfg.Backend.Call(r.Context(), protocol.MethodProfileStatus, statusParams)
	if perr != nil {
		return nil
	}
	var status protocol.ProfileStatusResult
	if err := json.Unmarshal(result, &status); err != nil {
		return nil
	}
	return profile.KnownDigests(status)
}

// pushRefusal turns a discovery failure into the sentence the dashboard
// shows. A scanner finding names the file, the line, and the terminal
// command that can override it; everything else speaks for itself.
func pushRefusal(root, harness string, err error) string {
	var de *profile.DiscoverError
	// Only a scanner finding carries a location. A symlink escape does
	// not, and --allow-secret cannot re-include one, so it gets no
	// override advice.
	if !errors.As(err, &de) || de.Location == "" {
		return err.Error()
	}
	return fmt.Sprintf("%s in %s:%s; remove it from %s, or push from a terminal with: "+
		"aether profile push --agent %s --allow-secret %s --workspace <workspace>",
		de.Message, de.Path, de.Location, root, harness, de.Path)
}
