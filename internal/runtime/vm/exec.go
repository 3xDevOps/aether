package vm

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/devexec"
	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const stdinCloseTimeout = 5 * time.Second

func processID(kind string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return kind + "-" + hex.EncodeToString(b[:])
}

// A root exec gets sudo's secure_path instead of the VM's PATH, as with
// Docker.
func execEnv(rec *record, user string, extra []string) []string {
	if uid, _, _ := strings.Cut(user, ":"); uid == "0" || uid == "root" {
		extra = append(append([]string{}, extra...), runtime.RootPath)
	}
	return overrideEnv(rec.Env, extra)
}

func overrideEnv(base, extra []string) []string {
	merged := make(map[string]string, len(base)+len(extra))
	for _, list := range [][]string{base, extra} {
		for _, pair := range list {
			if k, v, ok := strings.Cut(pair, "="); ok {
				merged[k] = v
			}
		}
	}
	return runtime.EnvList(merged)
}

func (e *engine) settingUp(key string) bool { return e.flag(e.booting, key) }

// attach replays Main's output from its start only to the first attach after
// this process booted the VM; later ones, like docker attach, see output from
// now on.
func (e *engine) attach(ctx context.Context, key string) (runtime.Attachment, error) {
	if _, err := e.requireRunning(ctx, key, "attach to"); err != nil {
		return nil, err
	}
	h, err := e.helper(key)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	fromStart := e.fresh[key]
	delete(e.fresh, key)
	e.mu.Unlock()
	return h.Process(guest.MainID).Attach(fromStart), nil
}

func (e *engine) spawn(ctx context.Context, key string, spec guest.ProcessSpec) (process, runtime.Attachment, error) {
	rec, err := e.requireRunning(ctx, key, "exec in")
	if err != nil {
		return nil, nil, err
	}
	// A paused guest would hold the exec until ctx ends; Docker refuses it.
	if e.paused(ctx, key) {
		return nil, nil, fmt.Errorf("vm runtime: exec in VM %s: it is paused", key)
	}
	h, err := e.helper(key)
	if err != nil {
		return nil, nil, err
	}
	if spec.User == "" {
		spec.User = rec.User
	}
	spec.Env = execEnv(rec, spec.User, spec.Env)
	if spec.Dir == "" {
		spec.Dir = rec.WorkingDir
	}
	p, err := h.Spawn(ctx, spec)
	if err != nil {
		return nil, nil, err
	}
	return p, p.Attach(true), nil
}

func (e *engine) exec(ctx context.Context, key string, argv []string, workDir string) (int, string, string, error) {
	p, att, err := e.spawn(ctx, key, guest.ProcessSpec{ID: processID("exec"), Argv: argv, Dir: workDir, Lossless: true})
	var start *guest.StartError
	if errors.As(err, &start) {
		return start.Code, "", err.Error(), nil
	}
	if err != nil {
		return 0, "", "", fmt.Errorf("vm runtime: exec: %w", err)
	}
	defer func() { _ = att.Close() }()
	if err = p.CloseStdin(ctx); err != nil {
		return 0, "", "", fmt.Errorf("vm runtime: exec input: %w", err)
	}
	stdout, stderr, err := readLimited(ctx, att)
	if err != nil {
		return 0, stdout, stderr, fmt.Errorf("vm runtime: exec output: %w", err)
	}
	code, err := p.Wait(ctx)
	if err != nil {
		return 0, stdout, stderr, fmt.Errorf("vm runtime: exec wait: %w", err)
	}
	return code, stdout, stderr, nil
}

// readLimited reads both streams to their end and refuses output past
// runtime.ExecOutputLimit, which the two share.
func readLimited(ctx context.Context, att runtime.Attachment) (string, string, error) {
	var mu sync.Mutex
	total := 0
	var out, errOut bytes.Buffer
	limited := func(into *bytes.Buffer) io.Writer {
		return writerFunc(func(p []byte) (int, error) {
			mu.Lock()
			defer mu.Unlock()
			if total += len(p); total > runtime.ExecOutputLimit {
				return 0, fmt.Errorf("output passed the %d byte limit", runtime.ExecOutputLimit)
			}
			return into.Write(p)
		})
	}
	err := copyStreams(ctx, att, limited(&out), limited(&errOut), func() { _ = att.Close() })
	return out.String(), errOut.String(), err
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// copyStreams copies stdout and stderr at once; the first failure, or the
// end of ctx, calls abort so the other copy is not left blocked on a stream
// the command keeps open.
func copyStreams(ctx context.Context, att runtime.Attachment, stdout, stderr io.Writer, abort func()) error {
	abort = sync.OnceFunc(abort)
	stop := context.AfterFunc(ctx, abort)
	defer stop()
	errs := make(chan error, 2)
	for _, pair := range [...]struct {
		w io.Writer
		r io.Reader
	}{{stdout, att.Stdout()}, {stderr, att.Stderr()}} {
		go func() {
			_, err := io.Copy(pair.w, pair.r)
			if err != nil {
				abort()
			}
			errs <- err
		}()
	}
	err := errors.Join(<-errs, <-errs)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

func (e *engine) execTTY(ctx context.Context, key string, argv []string, workDir string, cols, rows uint) (runtime.Attachment, error) {
	size := guest.Size{Cols: 80, Rows: 24}
	if cols != 0 && rows != 0 {
		size = guest.Size{Cols: uint16(min(cols, 65535)), Rows: uint16(min(rows, 65535))}
	}
	_, att, err := e.spawn(ctx, key, guest.ProcessSpec{ID: processID("tty"), Argv: argv, Dir: workDir, TTY: &size})
	var start *guest.StartError
	if errors.As(err, &start) && (start.Code == 126 || start.Code == 127) {
		return nil, &runtime.ExecExitError{Code: start.Code}
	}
	if err != nil {
		return nil, fmt.Errorf("vm runtime: exec: %w", err)
	}
	return att, nil
}

// execAttached keeps the staged helper's input open while it runs: closing
// it, when ctx ends or the output cannot be delivered, kills the command.
func (e *engine) execAttached(ctx context.Context, key string, spec runtime.AttachedExec) (int, error) {
	argv := append([]string{coordtransport.CLIPath, devexec.Command, devexec.Attached, strconv.Itoa(len(spec.Stdin))}, spec.Argv...)
	p, att, err := e.spawn(ctx, key, guest.ProcessSpec{ID: processID("attached"), Argv: argv, User: spec.User, Dir: spec.WorkingDir, Lossless: true})
	if err != nil {
		return 0, fmt.Errorf("vm runtime: exec: %w", err)
	}
	defer func() { _ = att.Close() }()
	closeStdin := sync.OnceFunc(func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stdinCloseTimeout)
		defer cancel()
		_ = p.CloseStdin(closeCtx)
	})
	stop := context.AfterFunc(ctx, closeStdin)
	defer stop()
	if len(spec.Stdin) > 0 {
		if _, err = att.Stdin().Write(spec.Stdin); err != nil {
			closeStdin()
			return 0, fmt.Errorf("vm runtime: exec input: %w", err)
		}
	}
	if err = copyStreams(ctx, att, spec.Stdout, spec.Stderr, closeStdin); err != nil {
		return 0, fmt.Errorf("vm runtime: exec output: %w", err)
	}
	code, err := p.Wait(ctx)
	if err != nil {
		return 0, fmt.Errorf("vm runtime: exec wait: %w", err)
	}
	return code, nil
}

func (e *engine) runAsRoot(ctx context.Context, h helper, argv []string) (int, string, error) {
	p, err := h.Spawn(ctx, guest.ProcessSpec{ID: processID("prepare"), Argv: argv, User: "0:0", Env: []string{runtime.RootPath}, Dir: "/", Lossless: true})
	if err != nil {
		return 0, "", err
	}
	att := p.Attach(true)
	defer func() { _ = att.Close() }()
	if err = p.CloseStdin(ctx); err != nil {
		return 0, "", err
	}
	stdout, stderr, err := readLimited(ctx, att)
	if err != nil {
		return 0, "", err
	}
	code, err := p.Wait(ctx)
	return code, stdout + stderr, err
}
