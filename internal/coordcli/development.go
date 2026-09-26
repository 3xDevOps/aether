package coordcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// The command table is closed: no method name or identity comes from JSON.
// Both request and response use protocol DTOs, including artifact-only captures.
type developmentOperation struct {
	group, name, method, help string
	call                      func(context.Context, string, string, []byte) (any, error)
}

func devOperation[P, R any](group, name, method, help string) developmentOperation {
	return developmentOperation{group, name, method, help, callDevelopment[P, R]}
}

var developmentOperations = []developmentOperation{
	devOperation[protocol.DevTerminalListParams, protocol.DevTerminalListResult]("terminal", "list", protocol.MethodDevTerminalList, "List the authoritative development terminals (not the primary harness). Fields: none; omit --params-file for {}."),
	devOperation[protocol.DevTerminalStartParams, protocol.DevTerminalStartResult]("terminal", "start", protocol.MethodDevTerminalStart, "Start an owned PTY process. Optional fields: name, command (argv array; empty starts the account shell), cols, rows. No viewer is needed."),
	devOperation[protocol.DevTerminalOutputParams, protocol.DevTerminalOutputResult]("terminal", "output", protocol.MethodDevTerminalOutput, "Read output history, not the current rendered screen. Fields: terminal_id, incarnation; optional after {epoch,sequence}, max_bytes (<=8192), format (text or raw). Raw data is bounded base64. Honor next, more, missing_cursor and truncated."),
	devOperation[protocol.DevTerminalScreenParams, protocol.DevTerminalScreenResult]("terminal", "screen", protocol.MethodDevTerminalScreen, "Read rendered cells, not a screenshot. Fields: terminal_id, incarnation; optional row_offset, column_offset, expected_screen_revision, max_cells (<=256). Continue with returned row+column and screen revision; restart paging if the revision changed. Honor protocol_error and unsupported_graphics."),
	devOperation[protocol.DevTerminalScreenshotParams, protocol.DevTerminalScreenshotResult]("terminal", "screenshot", protocol.MethodDevTerminalScreenshot, "Capture the rendered terminal. Fields: terminal_id, incarnation. Returns server artifact metadata/path, never image base64; inspect the image with your harness image reader before making visual claims."),
	devOperation[protocol.DevTerminalInputParams, protocol.DevTerminalInputResult]("terminal", "input", protocol.MethodDevTerminalInput, "Send admitted input. Fields: terminal_id, incarnation, control_session_id, control_generation, kind (text, paste, key, mouse); text, key, modifiers array or mouse {action,button,x,y,delta} as appropriate. Text is <=8192 bytes. Acquire control first."),
	devOperation[protocol.DevTerminalResizeParams, protocol.DevTerminalResizeResult]("terminal", "resize", protocol.MethodDevTerminalResize, "Explicitly resize the PTY. Fields: terminal_id, incarnation, control_session_id, control_generation, cols, rows (1..500). Acquire control first; attaching a viewer does not resize."),
	devOperation[protocol.DevTerminalWaitParams, protocol.DevTerminalWaitResult]("terminal", "wait", protocol.MethodDevTerminalWait, "Boundedly wait for process/output/screen state. Fields: terminal_id, incarnation, timeout_ms (0..30000); optional after_output {epoch,sequence}, after_screen_revision, contains, exit. Inspect matched, timed_out and process.exit_code; CLI success alone does not prove command success."),
	devOperation[protocol.DevTerminalStopParams, protocol.DevTerminalStopResult]("terminal", "stop", protocol.MethodDevTerminalStop, "Stop the owned process group, not just its viewer. Fields: terminal_id, incarnation, control_session_id, control_generation, timeout_ms (0..30000). Inspect stopped/timed_out. Verify and capture evidence before cleanup."),
	devOperation[protocol.DevBrowserStatusParams, protocol.DevBrowserStatusResult]("browser", "status", protocol.MethodDevBrowserStatus, "Inspect availability/reason, state (not_started, creating, running, paused, session_lost), running flag and session identity. Fields: none; omit --params-file for {}. A pending: session_id after failed creation is only a recovery fence: acquire its browser surface and explicitly reset, never treat it as a page session or silently retry open."),
	devOperation[protocol.DevBrowserOpenParams, protocol.DevBrowserOpenResult]("browser", "open", protocol.MethodDevBrowserOpen, "Open a page in the isolated headless companion. Fields: url, control_session_id, control_generation; optional session_id, width, height. First launch omits session_id, uses a nonempty controller ID and generation 0; the broker creates and acquires the surface. Later opens require session_id and an acquired fence. Returns page identity and control fence. Bootstrap is bounded to 90 seconds."),
	devOperation[protocol.DevBrowserPagesParams, protocol.DevBrowserPagesResult]("browser", "pages", protocol.MethodDevBrowserPages, "List browser pages and selected page. Fields: session_id."),
	devOperation[protocol.DevBrowserNavigateParams, protocol.DevBrowserNavigateResult]("browser", "navigate", protocol.MethodDevBrowserNavigate, "Navigate under control. Fields: session_id, page_id, page_revision, control_session_id, control_generation, timeout_ms (0..30000); url or direction (url, back, forward, reload). Re-observe returned page revision before acting."),
	devOperation[protocol.DevBrowserSnapshotParams, protocol.DevBrowserSnapshotResult]("browser", "snapshot", protocol.MethodDevBrowserSnapshot, "Read bounded DOM/accessibility nodes, not visual evidence. Fields: session_id, page_id, page_revision; optional max_nodes (<=128), max_chars (<=8192). Honor truncated; node IDs are revision-scoped."),
	devOperation[protocol.DevBrowserActionParams, protocol.DevBrowserActionResult]("browser", "action", protocol.MethodDevBrowserAction, "Interact under control. Fields: session_id, page_id, page_revision, control_session_id, control_generation, action (click, fill, select_option, key, scroll, text, pointer, touch, select). Optional node_id, viewport_id, text, key, modifiers, values, x, y, delta_x, delta_y, button, phase (down, move, up, cancel), timeout_ms (0..30000). Coordinate input must use the observed viewport_id; do not reuse stale nodes/revisions."),
	devOperation[protocol.DevBrowserScreenshotParams, protocol.DevBrowserScreenshotResult]("browser", "screenshot", protocol.MethodDevBrowserScreenshot, "Capture a browser page. Fields: session_id, page_id, page_revision; optional full_page. Returns private server artifact metadata/path, never image base64 or a public upload."),
	devOperation[protocol.DevBrowserViewportParams, protocol.DevBrowserViewportResult]("browser", "viewport", protocol.MethodDevBrowserViewport, "Resize browser viewport under control. Fields: session_id, page_id, page_revision, control_session_id, control_generation, width, height (1..4096). Use the returned viewport identity for later coordinate input."),
	devOperation[protocol.DevBrowserWaitParams, protocol.DevBrowserWaitResult]("browser", "wait", protocol.MethodDevBrowserWait, "Wait for a page condition. Fields: session_id, page_id, page_revision, condition (text, visible, hidden, url, load), timeout_ms (0..30000); optional node_id, text. Check matched/timed_out and re-observe."),
	devOperation[protocol.DevBrowserConsoleParams, protocol.DevBrowserConsoleResult]("browser", "console", protocol.MethodDevBrowserConsole, "Read bounded browser console entries. Fields: session_id, page_id, page_revision; optional after sequence, limit (<=100). Honor next, missing_cursor and truncated."),
	devOperation[protocol.DevBrowserNetworkParams, protocol.DevBrowserNetworkResult]("browser", "network", protocol.MethodDevBrowserNetwork, "Read bounded browser network observations. Fields: session_id, page_id, page_revision; optional after sequence, limit (<=100). Honor next, missing_cursor and truncated."),
	devOperation[protocol.DevBrowserResetParams, protocol.DevBrowserResetResult]("browser", "reset", protocol.MethodDevBrowserReset, "Explicitly reset browser state under control. Fields: session_id, control_session_id, control_generation. Old page/session/control fences must not be reused; discover the new session and acquire again."),
	devOperation[protocol.DevBrowserCloseParams, protocol.DevBrowserCloseResult]("browser", "close", protocol.MethodDevBrowserClose, "Close one page under control. Fields: session_id, page_id, page_revision, control_session_id, control_generation. This is not viewer detach."),
	devOperation[protocol.DevControlStatusParams, protocol.DevControlStatusResult]("control", "status", protocol.MethodDevControlStatus, "Inspect surface ownership. Fields: surface {kind (terminal or browser), id, incarnation}. The primary harness is never a development surface."),
	devOperation[protocol.DevControlAcquireParams, protocol.DevControlAcquireResult]("control", "acquire", protocol.MethodDevControlAcquire, "Acquire writable control. Fields: surface {kind,id,incarnation}, control_session_id (your opaque controller ID); optional expected_generation, takeover. Use the returned controller.control_generation for mutations; do not invent or silently steal a live lease."),
	devOperation[protocol.DevControlReleaseParams, protocol.DevControlReleaseResult]("control", "release", protocol.MethodDevControlRelease, "Release your exact controller lease. Fields: surface {kind,id,incarnation}, control_session_id, control_generation. Release does not stop the process/browser."),
	devOperation[protocol.DevArtifactListParams, protocol.DevArtifactListResult]("artifact", "list", protocol.MethodDevArtifactList, "List this run's private captures. Optional fields: after, limit (<=100). Honor next/truncated; omit --params-file for {}."),
	devOperation[protocol.DevArtifactGetParams, protocol.DevArtifactGetResult]("artifact", "get", protocol.MethodDevArtifactGet, "Resolve transient capture metadata and its read-only in-run path. Fields: artifact_id. This does not return image bytes or accept a host path."),
	devOperation[protocol.DevArtifactDeleteParams, protocol.DevArtifactDeleteResult]("artifact", "delete", protocol.MethodDevArtifactDelete, "Delete a transient capture explicitly. Fields: artifact_id. Retain required verification evidence before deleting; deleting this capture does not delete a retained packet copy."),
	devOperation[protocol.DevArtifactRetainParams, protocol.DevArtifactRetainResult]("artifact", "retain", protocol.MethodDevArtifactRetain, "Intentionally copy selected captures into a retained evidence packet before report/cleanup. Fields: artifact_ids (1..64), idempotency_key; optional verification_notes (<=4096 UTF-8 bytes). Inspect returned packet_id and pass it to report --evidence-ref; retention does not itself report an outcome or prove verification. Keep the same key and exact selection/notes when explicitly retrying an uncertain result. No automatic retry or public upload. Retained images use evidence read permissions, not transient session access."),
}

func developmentCommand(ctx context.Context, socket, group string, args []string, in io.Reader) (any, error) {
	if len(args) == 0 {
		return nil, usageError(group + " requires a subcommand; use " + group + " --help")
	}
	for _, op := range developmentOperations {
		if op.group != group || op.name != args[0] {
			continue
		}
		fs := newFlags(group + " " + op.name)
		file := fs.String("params-file", "", "read bounded JSON from FILE or - for stdin; default {}")
		fs.Bool("json", false, "machine-readable JSON envelope (always enabled)")
		if err := parseFlags(fs, args[1:]); err != nil {
			return nil, err
		}
		if fs.NArg() != 0 {
			return nil, usageError(group + " " + op.name + " takes only --params-file and --json")
		}
		data, err := readDevelopmentParams(*file, in)
		if err != nil {
			return nil, err
		}
		return op.call(ctx, socket, op.method, data)
	}
	return nil, usageError("unknown " + group + " command: " + args[0])
}

func readDevelopmentParams(file string, in io.Reader) ([]byte, error) {
	if file == "" {
		return []byte(`{}`), nil
	}
	if file != "-" {
		f, err := os.Open(file)
		if err != nil {
			return nil, fmt.Errorf("read development params: %w", err)
		}
		defer f.Close() //nolint:errcheck // read-only file
		in = f
	}
	data, err := io.ReadAll(io.LimitReader(in, protocol.MaxDevParamsBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read development params: %w", err)
	}
	if len(data) > protocol.MaxDevParamsBytes {
		return nil, usageError(fmt.Sprintf("development params exceed %d bytes", protocol.MaxDevParamsBytes))
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, usageError("development params must be a JSON object")
	}
	return data, nil
}

func callDevelopment[P, R any](ctx context.Context, socket, method string, data []byte) (any, error) {
	var params P
	if err := protocol.DecodeDevAgentParams(data, &params); err != nil {
		return nil, usageError(method + ": " + err.Error())
	}
	// JSON escaping can expand an otherwise bounded source file. Bound the
	// actual typed payload too, leaving room for the v3 envelope below 64 KiB.
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, usageError(method + ": " + err.Error())
	}
	if len(encoded) > protocol.MaxDevParamsBytes {
		return nil, usageError(method + ": encoded parameters exceed 48 KiB")
	}
	var result R
	if err := coordtransport.Call(ctx, socket, method, params, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func developmentHelp(command string) (string, bool) {
	parts := strings.Fields(command)
	if len(parts) == 0 || len(parts) > 2 {
		return "", false
	}
	var names []string
	for _, op := range developmentOperations {
		if op.group != parts[0] {
			continue
		}
		if len(parts) == 2 && op.name == parts[1] {
			return "usage: aether-internal " + command + " [--params-file FILE|-] [--json]\n\n" + op.help + "\n\nParameters are a strict JSON object, at most 48 KiB before and after encoding.\nOmitted --params-file sends {}; required fields are checked by the server.\nUnknown fields and run_id (including null/empty) are rejected. The mounted\nsocket supplies identity. Help describes syntax, not live availability;\ncheck aether-internal status. Results use the v3 JSON envelope.\n", true
		}
		names = append(names, op.name)
	}
	if len(parts) == 1 && len(names) > 0 {
		return "usage: aether-internal " + command + " <command> [--params-file FILE|-] [--json]\n\nCommands: " + strings.Join(names, ", ") + "\nRun aether-internal " + command + " <command> --help for fields and semantics.\nThis is syntax documentation, not a claim of live server capability.\n", true
	}
	return "", false
}
