package coordcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const maxHookWakeInputBytes = 32 << 10

// hookWakeResult is deliberately smaller than status: no peer-controlled
// metadata, mailbox bodies or acknowledgement tokens cross this boundary.
type hookWakeResult struct {
	WaitSupported    bool     `json:"wait_supported"`
	UnreadMessageIDs []string `json:"unread_message_ids"`
	WakeAdmitted     bool     `json:"wake_admitted"`
	Context          string   `json:"context"`
}

func hookWake(ctx context.Context, cfg Config) (int, error) {
	params, err := readHookWakeParams(cfg.In)
	if err != nil {
		return ExitUsage, fmt.Errorf("hook wake: %w", err)
	}
	var status protocol.CoordStatusResult
	// Unlike lifecycle hooks, this call must outlive the requested server wait.
	// The transport supplies the bounded wait plus framing margin, while the
	// caller's context can cancel it immediately on native session teardown.
	if err := coordtransport.Call(ctx, cfg.Socket, protocol.MethodCoordHookStatus, params, &status); err != nil {
		switch code := coordtransport.ErrorCode(err); code {
		case protocol.CodeMethodNotFound, protocol.CodeInvalidParams:
			return unsupportedHookWake(err)
		case protocol.CodeUnavailable:
			return ExitFailure, fmt.Errorf("hook wake: %w", err)
		default:
			return exitFor(code), fmt.Errorf("hook wake: %w", err)
		}
	}
	if !status.WaitSupported {
		return unsupportedHookWake(nil)
	}
	ids := status.UnreadMessageIDs
	if ids == nil {
		ids = []string{}
	}
	result := hookWakeResult{WaitSupported: true, UnreadMessageIDs: ids, WakeAdmitted: status.WakeAdmitted}
	if status.WakeAdmitted && len(ids) > 0 {
		result.Context = protocol.CoordInboxContext(len(ids))
	}
	if err := json.NewEncoder(cfg.Out).Encode(result); err != nil {
		return ExitFailure, fmt.Errorf("hook wake: write response: %w", err)
	}
	return ExitOK, nil
}

func unsupportedHookWake(cause error) (int, error) {
	message := "hook wake: server does not support native inbox waits; stop this receiver, upgrade Aether and relaunch the run; use aether-internal inbox meanwhile"
	if cause != nil {
		return ExitUsage, fmt.Errorf("%s: %w", message, cause)
	}
	return ExitUsage, errors.New(message)
}

func readHookWakeParams(in io.Reader) (protocol.CoordHookStatusParams, error) {
	params := protocol.CoordHookStatusParams{WaitSeconds: protocol.CoordMaxInboxWaitSeconds}
	data, err := io.ReadAll(io.LimitReader(in, maxHookWakeInputBytes+1))
	if err != nil {
		return params, fmt.Errorf("read JSON input: %w", err)
	}
	if len(data) > maxHookWakeInputBytes {
		return params, fmt.Errorf("JSON input exceeds %d bytes", maxHookWakeInputBytes)
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return params, errors.New("expected a JSON object with seen_message_ids and optional wait_seconds")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&params); err != nil {
		return params, fmt.Errorf("invalid JSON input: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return params, errors.New("expected exactly one JSON object")
	}
	if params.WaitSeconds < 0 || params.WaitSeconds > protocol.CoordMaxInboxWaitSeconds {
		return params, fmt.Errorf("wait_seconds must be between 0 and %d", protocol.CoordMaxInboxWaitSeconds)
	}
	if len(params.SeenMessageIDs) > protocol.CoordMaxUnread {
		return params, fmt.Errorf("seen_message_ids must contain at most %d IDs", protocol.CoordMaxUnread)
	}
	for _, id := range params.SeenMessageIDs {
		if !protocol.ValidCoordMessageID(id) {
			return params, errors.New("seen_message_ids contains an invalid message ID")
		}
	}
	return params, nil
}
