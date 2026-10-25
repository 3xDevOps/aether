package coordcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func inbox(ctx context.Context, cfg Config, args []string) (int, error) {
	fs := newFlags("inbox")
	wait := fs.Int("wait", 0, "bounded wait in seconds")
	ack := fs.String("ack", "", "acknowledge this batch token instead of the last batch read")
	if err := parseFlags(fs, args); err != nil {
		return fail(cfg.Out, errorCode(err), err.Error())
	}
	if fs.NArg() != 0 {
		return fail(cfg.Out, protocol.CodeInvalidParams, "inbox takes --wait and --ack flags")
	}
	path := filepath.Join(os.TempDir(), "aether-inbox-"+os.Getenv(coordtransport.RunIDEnv)+"-ack")
	if *ack == "" {
		last, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fail(cfg.Out, protocol.CodeInternal, fmt.Sprintf("inbox: read the last batch's ack token: %v", err))
		}
		*ack = string(last)
	}
	var out protocol.CoordInboxResult
	if err := coordtransport.Call(ctx, cfg.Socket, protocol.MethodCoordInbox, protocol.CoordInboxParams{AckToken: *ack, WaitSeconds: *wait}, &out); err != nil {
		return fail(cfg.Out, errorCode(err), err.Error())
	}
	if out.Messages == nil {
		out.Messages = []protocol.CoordMessage{}
	}
	if err := writeEnvelope(cfg.Out, Envelope{SchemaVersion: SchemaVersion, OK: true, Result: out}); err != nil {
		return ExitFailure, fmt.Errorf("write success response: %w", err)
	}
	// Saved only once the batch is printed: a read that never reached the
	// agent leaves the previous token, so the next read delivers it again.
	if err := os.WriteFile(path, []byte(out.AckToken), 0o600); err != nil {
		return ExitFailure, fmt.Errorf("inbox: save ack token %q for the next read: %w", out.AckToken, err)
	}
	return ExitOK, nil
}

type ackResult struct {
	Acked   string `json:"acked"`
	Waiting int    `json:"waiting"`
}

func ack(ctx context.Context, socket string, args []string) (ackResult, error) {
	fs := newFlags("ack")
	if err := parseFlags(fs, args); err != nil {
		return ackResult{}, err
	}
	if fs.NArg() != 1 || fs.Arg(0) == "" {
		return ackResult{}, usageError("ack requires the ack_token of the batch you handled")
	}
	token := fs.Arg(0)
	var next protocol.CoordInboxResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodCoordInbox, protocol.CoordInboxParams{AckToken: token}, &next); err != nil {
		return ackResult{}, err
	}
	if !next.Acked {
		if next.AckToken == "" {
			return ackResult{}, &protocol.Error{Code: protocol.CodeConflict, Message: fmt.Sprintf("ack: %s is not the current batch and no inbox batch is waiting", token)}
		}
		return ackResult{}, &protocol.Error{Code: protocol.CodeConflict, Message: fmt.Sprintf("ack: %s is not the current batch; run inbox to read batch %s", token, next.AckToken)}
	}
	return ackResult{Acked: token, Waiting: len(next.Messages)}, nil
}
