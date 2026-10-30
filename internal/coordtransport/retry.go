package coordtransport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// A server restart is a stop, a re-exec or start of the new binary, and the
// listeners rebinding before the scheduler reattaches agents. A plain
// systemd restart or serverupdate re-exec rebinds the run's socket within a
// few seconds; a boot that applies store migrations takes longer, and
// serverupdate gives the new binary five minutes (trialDeadline) before it
// rolls back. The window covers the common case and a migrating boot
// without leaving an agent's tool call hanging for the full trial: past it
// the agent gets CodeUnavailable and decides.
const (
	retryWindow = 60 * time.Second
	// A stopped server leaves its socket files on disk, so a restart shows
	// as a refused connection. A path that does not exist at all is a CLI
	// outside a run, or a rebind caught between unlink and listen; it gets
	// a short grace and no more.
	missingSocketWindow = 2 * time.Second
	retryInitialDelay   = 100 * time.Millisecond
	retryMaxDelay       = 2 * time.Second
)

// transportFailure is a socket-level failure: nothing the server said.
type transportFailure struct {
	cause    error
	timedOut bool
}

func (f *transportFailure) Error() string { return f.cause.Error() }
func (f *transportFailure) Unwrap() error { return f.cause }

// retryable reports whether err could clear once the server is back. A call
// that ran out its own deadline is not retried: the server is up and slow.
func retryable(err error) bool {
	var f *transportFailure
	return errors.As(err, &f) && !f.timedOut
}

func windowFor(err error) time.Duration {
	if errors.Is(err, fs.ErrNotExist) {
		return missingSocketWindow
	}
	return retryWindow
}

// repeatable reports whether sending the same request twice is safe once
// the first may have been processed: it only reads, the server dedupes it
// durably on the idempotency key it carries, or its effect is the same
// when repeated.
func repeatable(method string, params json.RawMessage) bool {
	if repeatableMethods[method] {
		return true
	}
	var p struct {
		Key string `json:"idempotency_key"`
	}
	return json.Unmarshal(params, &p) == nil && p.Key != ""
}

// repeatableMethods are the methods that read, or whose repeat is absorbed
// server-side without a key. The inbox is among them: an unacknowledged
// batch is stored with its ack token and delivered again until acked, so
// a read cut off mid-response loses and duplicates nothing. memory.add
// stores a note once per text and answers a repeat with the live note.
var repeatableMethods = map[string]bool{
	protocol.MethodCoordStatus: true, protocol.MethodCoordHookStatus: true, protocol.MethodCoordInbox: true,
	protocol.MethodMemoryAdd: true, protocol.MethodMemorySearch: true, protocol.MethodMemoryShow: true, protocol.MethodMemoryBrief: true,
	protocol.MethodChildOptions: true, protocol.MethodChildList: true, protocol.MethodChildFetch: true,
	protocol.MethodTaskShow: true, protocol.MethodTaskList: true, protocol.MethodWorkerList: true, protocol.MethodWorkerInspect: true,
	protocol.MethodMissionPlanShow: true, protocol.MethodIntegrationShow: true,
	protocol.MethodDevTerminalList: true, protocol.MethodDevTerminalOutput: true, protocol.MethodDevTerminalScreen: true,
	protocol.MethodDevTerminalScreenshot: true, protocol.MethodDevTerminalWait: true,
	protocol.MethodDevBrowserStatus: true, protocol.MethodDevBrowserPages: true, protocol.MethodDevBrowserSnapshot: true,
	protocol.MethodDevBrowserInspect: true, protocol.MethodDevBrowserScreenshot: true, protocol.MethodDevBrowserConsole: true,
	protocol.MethodDevBrowserNetwork: true, protocol.MethodDevControlStatus: true,
	protocol.MethodDevArtifactList: true, protocol.MethodDevArtifactGet: true, protocol.MethodDevPortList: true,
}

// ambiguous is the failure of a request that was sent, then lost its
// connection, and cannot be repeated blindly.
func ambiguous(method string, cause error) *coordError {
	return &coordError{
		Code: protocol.CodeUnavailable,
		Message: fmt.Sprintf("%s: coordination is not reachable: %v; the request was sent before the connection failed, "+
			"so it may already have applied; check its effect before repeating it", method, cause),
	}
}

func unavailableSent(method string, cause error) *coordError {
	return &coordError{
		Code: protocol.CodeUnavailable,
		Message: fmt.Sprintf("%s: coordination is not reachable after retrying: %v; the request was sent and may already have applied",
			method, cause),
	}
}

func unavailableUnsent(method string, cause error) *coordError {
	return &coordError{
		Code: protocol.CodeUnavailable,
		Message: fmt.Sprintf("%s: coordination is not reachable after retrying: %v; the request was not delivered and nothing applied",
			method, cause),
	}
}
