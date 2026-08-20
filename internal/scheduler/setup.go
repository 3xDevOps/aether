package scheduler

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// SetupLogin starts a short-lived login container with the member's
// harness credential home mounted, attaches PTY-like stdio to conn, and
// destroys the container on detach or error. No Run row is created.
func (s *Scheduler) SetupLogin(ctx context.Context, member domain.MemberID, harnessName, image string, cols, rows uint, conn io.ReadWriter, resize <-chan [2]uint) error {
	profile, ok := harness.Lookup(harnessName)
	if !ok {
		return fmt.Errorf("scheduler: unknown harness %q", harnessName)
	}
	image, err := s.resolveSetupImage(ctx, image)
	if err != nil {
		return err
	}

	user, err := s.resolveContainerUser(ctx, image, profile)
	if err != nil {
		return fmt.Errorf("scheduler: resolve setup user: %w", err)
	}
	containerHome := harness.HomeDir(user)
	setupRun := &domain.Run{}
	mounts, err := s.credentialMounts(setupRun, member, profile, containerHome)
	if err != nil {
		return fmt.Errorf("scheduler: credential mounts: %w", err)
	}
	reservation, err := s.reserveCredentialUser(member, harnessName, user, len(mounts) > 0, "live setup session", nil)
	if err != nil {
		return fmt.Errorf("scheduler: reserve setup user: %w", err)
	}
	defer s.releaseCredentialUser(reservation)
	if applyErr := s.applyRunOwnership(nil, setupRun, mounts, user); applyErr != nil {
		return fmt.Errorf("scheduler: apply setup ownership: %w", applyErr)
	}

	id, err := s.cfg.Runtime.Create(ctx, runtime.Spec{
		Name:        "setup-" + string(member) + "-" + harnessName,
		Image:       image,
		Command:     []string{"/bin/sh", "-i"},
		TTY:         true,
		Mounts:      mounts,
		User:        user,
		Env:         map[string]string{"HOME": containerHome, "TERM": "xterm-256color", "PS1": "aether-setup$ "},
		CreationKey: fmt.Sprintf("setup-%s-%s-%d", member, harnessName, time.Now().UnixNano()),
	})
	if err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.cfg.Runtime.Stop(cctx, id, 2*time.Second)
		_ = s.cfg.Runtime.Destroy(cctx, id)
	}()
	if err = s.cfg.Runtime.Start(ctx, id); err != nil {
		return err
	}
	att, err := s.cfg.Runtime.Attach(ctx, id)
	if err != nil {
		return err
	}
	defer func() { _ = att.Close() }()
	if cols > 0 && rows > 0 {
		_ = att.Resize(ctx, cols, rows)
	}

	done := make(chan struct{})
	defer close(done)
	if resize != nil {
		go func() {
			for {
				select {
				case sz := <-resize:
					_ = att.Resize(ctx, sz[0], sz[1])
				case <-done:
					return
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// halfCloseConn signals end-of-output to the client. An SSH-backed
	// conn half-closes so the subsystem handler can still deliver the
	// exit status; a plain conn is closed outright.
	halfCloseConn := func() {
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
			return
		}
		if closer, ok := conn.(io.Closer); ok {
			_ = closer.Close()
		}
	}
	outputDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(conn, att.Stdout())
		close(outputDone)
	}()
	inputDone := make(chan struct{})
	stdin := att.Stdin()
	go func() {
		_, _ = io.Copy(stdin, conn)
		_ = stdin.Close()
		close(inputDone)
	}()
	select {
	case <-outputDone:
		halfCloseConn()
	case <-inputDone:
	case <-ctx.Done():
		halfCloseConn()
		return ctx.Err()
	}
	return nil
}

// resolveSetupImage turns the caller's image into a workspace image. The
// value never reaches the runtime unchecked: a login container is an
// interactive shell on the host's container daemon, so the image is a
// selector over the images an admin already registered, not something a
// client may name. An empty selector takes the first registered image.
func (s *Scheduler) resolveSetupImage(ctx context.Context, image string) (string, error) {
	workspaces, err := s.cfg.Store.ListWorkspaces(ctx)
	if err != nil {
		return "", fmt.Errorf("scheduler: list workspaces: %w", err)
	}
	for _, ws := range workspaces {
		if ws.Image != "" && (image == "" || ws.Image == image) {
			return ws.Image, nil
		}
	}
	if image != "" {
		return "", fmt.Errorf("scheduler: setup image %q is not a workspace image", image)
	}
	return "", fmt.Errorf("scheduler: setup requires an image")
}
