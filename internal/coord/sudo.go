package coord

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// SudoRunner runs a run's sudo command and reports its exit code. A
// *protocol.Error is a refusal the shim prints as it is.
type SudoRunner interface {
	Sudo(ctx context.Context, run domain.RunID, p protocol.RunSudoParams, stdin []byte, stdout, stderr io.Writer) (int, error)
}

// serveSudo answers run.sudo, the last request on its connection. The
// command's input follows the request line; its output goes back as
// run.sudo.output notifications ahead of the response. The command lives no
// longer than the connection or the run.
func (s *Service) serveSudo(ctx context.Context, conn net.Conn, r *bufio.Reader, capped *capReader, run domain.RunID, req protocol.Request, resp protocol.Response) {
	stream := &sudoStream{conn: conn, idle: s.cfg.idle}
	code, rpcErr := s.runSudo(ctx, run, req, r, capped, stream)
	if rpcErr == nil {
		resp.Result, _ = json.Marshal(protocol.RunSudoResult{ExitCode: code})
	}
	resp.Error = rpcErr
	_ = stream.write(resp)
}

func (s *Service) runSudo(ctx context.Context, run domain.RunID, req protocol.Request, r *bufio.Reader, capped *capReader, stream *sudoStream) (int, *protocol.Error) {
	if s.cfg.Sudo == nil {
		return 0, &protocol.Error{Code: protocol.CodeUnavailable, Message: req.Method + ": sudo is unavailable on this server"}
	}
	p, rpcErr := decodeParams[protocol.RunSudoParams](req.Method, req.Params)
	if rpcErr != nil {
		return 0, rpcErr
	}
	if len(p.Argv) == 0 || p.Argv[0] == "" {
		return 0, invalidParams(req.Method, "argv needs a command")
	}
	if p.StdinBytes < 0 || p.StdinBytes > protocol.RunSudoMaxStdin {
		return 0, invalidParams(req.Method, fmt.Sprintf("stdin_bytes must be 0 to %d", protocol.RunSudoMaxStdin))
	}
	capped.left = p.StdinBytes
	stdin := make([]byte, p.StdinBytes)
	if _, err := io.ReadFull(r, stdin); err != nil {
		return 0, invalidParams(req.Method, "read the command's input: "+err.Error())
	}
	if !s.enterRun(run) {
		return 0, runClosing(req.Method)
	}
	defer s.leaveRun(run)
	s.mu.Lock()
	closing := s.runs[run].ctx
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(closing, cancel)()
	code, err := s.cfg.Sudo.Sudo(ctx, run, p, stdin, sudoOutput{stream, false}, sudoOutput{stream, true})
	var refusal *protocol.Error
	switch {
	case errors.As(err, &refusal):
		return 0, refusal
	case err != nil:
		return 0, internalError(req.Method, err)
	}
	return code, nil
}

// sudoStream writes run.sudo's lines, from the command's stdout and stderr
// at once.
type sudoStream struct {
	mu   sync.Mutex
	conn net.Conn
	idle time.Duration
}

func (w *sudoStream) write(v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err = w.conn.SetWriteDeadline(time.Now().Add(w.idle)); err != nil {
		return err
	}
	_, err = w.conn.Write(append(line, '\n'))
	return err
}

type sudoOutput struct {
	stream *sudoStream
	stderr bool
}

func (o sudoOutput) Write(p []byte) (int, error) {
	frame := struct {
		JSONRPC string                 `json:"jsonrpc"`
		Method  string                 `json:"method"`
		Params  protocol.RunSudoOutput `json:"params"`
	}{"2.0", protocol.MethodRunSudoOutput, protocol.RunSudoOutput{Stderr: o.stderr, Data: p}}
	if err := o.stream.write(frame); err != nil {
		return 0, err
	}
	return len(p), nil
}
