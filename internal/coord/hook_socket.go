package coord

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const hookWriteTimeout = 3 * time.Second

func hookWaitRequest(req protocol.Request) bool {
	raw := bytes.TrimSpace(req.Params)
	return req.Method == protocol.MethodCoordHookStatus && len(raw) != 0 && !bytes.Equal(raw, []byte("null"))
}

func (s *Service) hookStatus(ctx context.Context, run domain.RunID, p protocol.CoordHookStatusParams) (protocol.CoordStatusResult, *protocol.Error) {
	if !s.enterRun(run) {
		return protocol.CoordStatusResult{}, runClosing(protocol.MethodCoordHookStatus)
	}
	defer s.leaveRun(run)
	observed, rpcErr := s.observeHook(ctx, run, p)
	defer s.releaseHookWaiter(run, observed.waiter)
	return observed.status, rpcErr
}

// serveHookWait is the only path that can claim wake_admitted. It retains both
// the observer and the run reference until a complete response line has been
// accepted. A preflight eligibility flag is deliberately insufficient.
func (s *Service) serveHookWait(ctx context.Context, conn net.Conn, run domain.RunID, req protocol.Request, resp protocol.Response) error {
	params, rpcErr := decodeParams[protocol.CoordHookStatusParams](req.Method, req.Params)
	if rpcErr != nil {
		resp.Error = rpcErr
		return writeHookResponse(conn, resp)
	}
	if !s.enterRun(run) {
		resp.Error = runClosing(req.Method)
		return writeHookResponse(conn, resp)
	}
	defer s.leaveRun(run)
	observed, rpcErr := s.observeHook(ctx, run, params)
	defer s.releaseHookWaiter(run, observed.waiter)
	if rpcErr != nil {
		resp.Error = rpcErr
		return writeHookResponse(conn, resp)
	}
	status := observed.status
	writeStatus := func() error {
		raw, err := json.Marshal(status)
		if err != nil {
			return err
		}
		resp.Result = raw
		return writeHookResponse(conn, resp)
	}
	if status.WaitSupported && len(status.UnreadMessageIDs) > 0 && s.cfg.WakeAdmission != nil {
		attempted := false
		var writeErr error
		// Admission failures suppress native action, not durable mail. If the
		// callback starts a write, never append a second (false) response after
		// a partial frame or an admission implementation's late error.
		_ = s.cfg.WakeAdmission(ctx, run, func() error {
			if ctx.Err() != nil || s.isRunClosing(run) {
				return errWakeSuppressed
			}
			mail := s.cfg.Mail.(store.UnackedRunMessageIDsStore)
			ids, err := mail.ListUnackedRunMessageIDs(ctx, run, protocol.CoordMaxUnread)
			if err != nil {
				return err
			}
			status.UnreadMessageIDs, status.Unread = ids, len(ids)
			admission := s.inboxAdmissionLock(run)
			admission.Lock()
			defer admission.Unlock()
			if ctx.Err() != nil || s.isRunClosing(run) || s.hookConsumerWon(run, observed.waiter) {
				return errWakeSuppressed
			}
			if len(ids) == 0 {
				return errWakeSuppressed
			}
			status.WakeAdmitted = true
			attempted = true
			writeErr = writeStatus()
			return writeErr
		})
		if attempted {
			return writeErr
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return writeStatus()
}

func writeHookResponse(conn net.Conn, resp protocol.Response) error {
	out, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(time.Now().Add(hookWriteTimeout)); err != nil {
		return err
	}
	out = append(out, '\n')
	for len(out) > 0 {
		n, err := conn.Write(out)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		out = out[n:]
	}
	return nil
}
