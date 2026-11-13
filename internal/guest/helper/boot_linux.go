package helper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/guest/guestpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	dockerSocket        = "/var/run/docker.sock"
	dockerdLog          = "/var/log/aether-dockerd.log"
	dockerdStartTimeout = 60 * time.Second
)

// boot is Docker's start of a container: dockerd first, then the setup
// script, then the main process, each only when the one before succeeded.
func (s *service) boot(req *guestpb.BootRequest) (*guestpb.BootReply, error) {
	main := processSpec(req.GetMain())
	main.ID = guest.MainID
	if err := checkSpec(main); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "main process: %v", err)
	}
	if s.cgroup != nil {
		if err := s.cgroup.setLimit(req.GetPidsLimit()); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	// Docker creates a container's missing working directory.
	if main.Dir != "" {
		if err := os.MkdirAll(main.Dir, 0o755); err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "working directory: %v", err)
		}
	}
	if req.GetNestedDocker() {
		if err := s.startDockerd(main); err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
	}
	if script := req.GetSetupScript(); script != "" {
		reply, err := s.setup(main, script)
		if err != nil || reply.GetSetupExitCode() != 0 {
			return reply, err
		}
	}
	if _, err := s.procs.spawn(spawnSpec{ProcessSpec: main}); err != nil {
		return nil, rpcError(err)
	}
	return &guestpb.BootReply{}, nil
}

// setup runs the script with the main process's user, environment and
// directory; its stderr joins stdout so the end of the output is where it
// failed.
func (s *service) setup(main guest.ProcessSpec, script string) (*guestpb.BootReply, error) {
	spec := main
	spec.ID, spec.TTY, spec.Lossless = guest.SetupID, nil, false
	spec.Argv = []string{"/bin/sh", "-ec", "exec 2>&1; " + script}
	p, err := s.procs.spawn(spawnSpec{ProcessSpec: spec})
	if err != nil {
		var failed *execError
		if errors.As(err, &failed) {
			return &guestpb.BootReply{SetupExitCode: int32(failed.code), SetupOutput: []byte(failed.Error())}, nil
		}
		return nil, rpcError(err)
	}
	// Docker gives setup no input, so a prompt reads end of file instead of
	// holding Boot forever.
	if err := p.closeInput(); err != nil {
		return nil, rpcError(err)
	}
	<-p.done
	p.stdout.waitClosed()
	if p.code == 0 {
		return nil, nil
	}
	output, cut := p.stdout.last(guest.SetupOutputLimit)
	return &guestpb.BootReply{SetupExitCode: int32(p.code), SetupOutput: output, SetupOutputCut: cut}, nil
}

// startDockerd starts the image's dockerd when it has dockerd and docker,
// with the socket's group set to the main process's so its user reaches it,
// and nested containers under the session's process limit.
func (s *service) startDockerd(main guest.ProcessSpec) error {
	_, home, err := credential("0")
	if err != nil {
		return err
	}
	// dockerd runs as root, so it is never looked up on the session's PATH,
	// which starts in the member's writable home.
	env := withDefaults(slices.DeleteFunc(slices.Clone(main.Env), func(e string) bool { return strings.HasPrefix(e, "PATH=") }), home, false)
	for _, name := range []string{"dockerd", "docker"} {
		if _, err = lookPath(name, defaultPath, "/"); err != nil {
			return nil
		}
	}
	cred, _, err := credential(main.User)
	if err != nil {
		return err
	}
	group := "0"
	if cred != nil {
		group = strconv.FormatUint(uint64(cred.Gid), 10)
	}
	// A saved environment can hold a stopped daemon's files.
	for _, stale := range []string{"/var/run/docker.pid", dockerSocket, "/var/run/docker/containerd/containerd.pid", "/var/run/docker/containerd/containerd.sock"} {
		_ = os.Remove(stale)
	}
	log, err := os.OpenFile(dockerdLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return fmt.Errorf("open %s: %w", dockerdLog, err)
	}
	defer func() { _ = log.Close() }()
	argv := []string{"dockerd", "-H", "unix://" + dockerSocket, "--group", group}
	if s.cgroup != nil {
		argv = append(argv, "--cgroup-parent", sessionCgroupParent)
	}
	p, err := s.procs.spawn(spawnSpec{ProcessSpec: guest.ProcessSpec{ID: dockerdID, Argv: argv, Env: env}, log: log})
	if err != nil {
		return fmt.Errorf("start dockerd: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), dockerdStartTimeout)
	defer cancel()
	for {
		if dockerAnswers(ctx) {
			return nil
		}
		select {
		case <-p.done:
			return fmt.Errorf("dockerd exited %d: %s", p.code, logTail(dockerdLog))
		case <-ctx.Done():
			return fmt.Errorf("dockerd did not answer within %s: %s", dockerdStartTimeout, logTail(dockerdLog))
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func dockerAnswers(ctx context.Context) bool {
	client := http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", dockerSocket)
		},
	}}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/_ping", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// logTail is the end of a log, where a failure is.
func logTail(name string) string {
	f, err := os.Open(name)
	if err != nil {
		return err.Error()
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err == nil && info.Size() > 4<<10 {
		_, _ = f.Seek(-4<<10, io.SeekEnd)
	}
	data, _ := io.ReadAll(f)
	return strings.TrimSpace(string(data))
}
