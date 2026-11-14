package helper

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/guest/guestpb"
	"github.com/mutagen-io/mutagen/pkg/encoding"
	"github.com/mutagen-io/mutagen/pkg/logging"
	"github.com/mutagen-io/mutagen/pkg/synchronization"
	"github.com/mutagen-io/mutagen/pkg/synchronization/endpoint/remote"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// target is where the session sees key's checkout, which its owner can
// reach.
func (s *gitService) target(key string) string {
	if t, ok := s.targets[key]; ok {
		return t
	}
	return filepath.Join(s.dir, key)
}

// ownerGit runs git in dir as the user who owns dir, so every file it
// writes stays the session's. That user could run git there anyway; hooks
// and fsmonitor are still turned off, as everywhere the server runs git.
func ownerGit(ctx context.Context, dir string, stdin io.Reader, args ...string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	st := info.Sys().(*syscall.Stat_t)
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)...)
	cmd.Env = append([]string{"PATH=" + defaultPath, "HOME=/", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}, gitengine.ConfigEnv(gitLimits)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: st.Uid, Gid: st.Gid, NoSetGroups: true}}
	cmd.Stdin = stdin
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *gitService) Stage(ctx context.Context, req *guestpb.StageRequest) (*guestpb.StageReply, error) {
	if _, err := s.workspace(req.GetKey()); err != nil {
		return nil, err
	}
	evidence := req.GetPurpose() == guestpb.StageRequest_PURPOSE_EVIDENCE
	tree, head, err := s.engine.StageCheckout(ctx, domain.RunID(req.GetKey()), evidence)
	if err != nil {
		return nil, gitStatus(err)
	}
	return &guestpb.StageReply{Tree: tree, Head: head}, nil
}

// Install indexes the pack the server streams into the checkout, applies
// the ref updates in one transaction and installs the commit index.
func (s *gitService) Install(stream grpc.ClientStreamingServer[guestpb.InstallChunk, guestpb.InstallReply]) error {
	ctx := stream.Context()
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	key := first.GetKey()
	if _, err = s.workspace(key); err != nil {
		return err
	}
	dir := s.target(key)
	// A commit's tree was staged into the service's own store; the
	// checkout takes its objects from there, the commit from the server.
	if tree := first.GetIndex(); tree != "" {
		var haves []string
		for _, u := range first.GetUpdates() {
			if u.GetOld() != "" {
				haves = append(haves, u.GetOld())
			}
		}
		staged, stagedWriter := io.Pipe()
		go func() {
			_ = stagedWriter.CloseWithError(s.engine.PackObjects(ctx, domain.RunID(key), []string{tree}, haves, stagedWriter))
		}()
		err = ownerGit(ctx, dir, staged, "index-pack", "--stdin", "--strict")
		_ = staged.CloseWithError(io.ErrClosedPipe)
		if err != nil {
			return gitStatus(fmt.Errorf("index the staged objects: %w", err))
		}
	}
	pack, writer := io.Pipe()
	go func() {
		data := first.GetData()
		for {
			if len(data) > 0 {
				if _, writeErr := writer.Write(data); writeErr != nil {
					return
				}
			}
			chunk, recvErr := stream.Recv()
			if errors.Is(recvErr, io.EOF) {
				_ = writer.Close()
				return
			}
			if recvErr != nil {
				_ = writer.CloseWithError(recvErr)
				return
			}
			data = chunk.GetData()
		}
	}()
	// The server's pack is trusted, so it is checked but not bounded.
	err = ownerGit(ctx, dir, pack, "index-pack", "--stdin", "--strict")
	_ = pack.CloseWithError(io.ErrClosedPipe)
	if err != nil {
		return gitStatus(fmt.Errorf("index the server's objects: %w", err))
	}
	if updates := first.GetUpdates(); len(updates) > 0 {
		var tx strings.Builder
		tx.WriteString("start\n")
		for _, u := range updates {
			fmt.Fprintf(&tx, "update %s %s %s\n", u.GetRef(), u.GetNew(), u.GetOld())
		}
		tx.WriteString("prepare\ncommit\n")
		if err := ownerGit(ctx, dir, strings.NewReader(tx.String()), "update-ref", "--stdin", "-m", first.GetReflogMessage()); err != nil {
			return gitStatus(err)
		}
	}
	if tree := first.GetIndex(); tree != "" {
		if err := s.installIndex(ctx, key, dir, tree); err != nil {
			return gitStatus(err)
		}
	}
	return stream.SendAndClose(&guestpb.InstallReply{})
}

// installIndex replaces the checkout's index by the one a commit was staged
// in. It writes beneath the checkout, so a .git the session made a symlink
// cannot send the write elsewhere.
func (s *gitService) installIndex(ctx context.Context, key, dir, tree string) error {
	data, err := s.engine.CommitIndex(ctx, domain.RunID(key), tree)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Stat(".git")
	if err != nil {
		return err
	}
	st := info.Sys().(*syscall.Stat_t)
	tmp := ".git/index.aether"
	_ = root.Remove(tmp)
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Chown(int(st.Uid), int(st.Gid))
	}
	if err = errors.Join(err, f.Close()); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return root.Rename(tmp, ".git/index")
}

func (s *gitService) Tree(ctx context.Context, req *guestpb.TreeRequest) (*guestpb.TreeReply, error) {
	if _, err := s.workspace(req.GetKey()); err != nil {
		return nil, err
	}
	entries, err := s.engine.ListTree(ctx, filepath.Join(s.dir, req.GetKey()), "", req.GetDir())
	if err != nil {
		return nil, gitStatus(err)
	}
	out := &guestpb.TreeReply{Entries: make([]*guestpb.TreeEntry, len(entries))}
	for i, e := range entries {
		out.Entries[i] = &guestpb.TreeEntry{Name: e.Name, Kind: e.Kind, Size: e.Size}
	}
	return out, nil
}

func (s *gitService) ReadFile(req *guestpb.ReadFileRequest, stream grpc.ServerStreamingServer[guestpb.FileChunk]) error {
	if _, err := s.workspace(req.GetKey()); err != nil {
		return err
	}
	file, info, err := s.engine.OpenCheckoutFile(filepath.Join(s.dir, req.GetKey()), req.GetPath())
	if err != nil {
		return gitStatus(err)
	}
	defer func() { _ = file.Close() }()
	limit := io.LimitReader(file, int64(min(max(req.GetMaxBytes(), 1), guest.MaxFileBytes)))
	buf := make([]byte, guest.ObjectsChunk)
	chunk := &guestpb.FileChunk{Size: info.Size()}
	for {
		n, readErr := io.ReadFull(limit, buf)
		chunk.Data = buf[:n]
		if err := stream.Send(chunk); err != nil {
			return err
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		chunk = &guestpb.FileChunk{}
	}
}

func (s *gitService) WriteFile(stream grpc.ClientStreamingServer[guestpb.WriteFileChunk, guestpb.WriteFileReply]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	ws, err := s.workspace(first.GetKey())
	if err != nil {
		return err
	}
	content := &chunkReader{data: first.GetData(), next: func() ([]byte, error) {
		chunk, recvErr := stream.Recv()
		return chunk.GetData(), recvErr
	}}
	if _, err = s.engine.WriteCheckoutFile(ws, domain.RunID(first.GetKey()), first.GetPath(), content, first.GetRevision()); err != nil {
		return gitStatus(err)
	}
	return stream.SendAndClose(&guestpb.WriteFileReply{})
}

// chunkReader reads the data of a stream's chunks in order.
type chunkReader struct {
	data []byte
	next func() ([]byte, error)
}

func (r *chunkReader) Read(p []byte) (int, error) {
	for len(r.data) == 0 {
		data, err := r.next()
		if err != nil {
			return 0, err
		}
		r.data = data
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func (s *gitService) FileDiff(ctx context.Context, req *guestpb.FileDiffRequest) (*guestpb.PatchReply, error) {
	if _, err := s.workspace(req.GetKey()); err != nil {
		return nil, err
	}
	p, err := s.engine.FileDiff(ctx, domain.RunID(req.GetKey()), req.GetPath())
	if err != nil {
		return nil, gitStatus(err)
	}
	return &guestpb.PatchReply{Base: p.Base, Text: p.Text, Truncated: p.Truncated, ForkTree: p.ForkTree}, nil
}

// Sync serves a live overlay's endpoint over the stream. The server pinned
// everything the endpoint touches; the endpoint's root is pinned here again,
// to the checkout, so nothing the stream says reaches past it.
func (s *gitService) Sync(stream grpc.BidiStreamingServer[guestpb.SyncChunk, guestpb.SyncChunk]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if _, err := s.workspace(first.GetKey()); err != nil {
		return err
	}
	conn := &syncConn{stream: stream, pending: first.GetData(), root: s.target(first.GetKey())}
	return remote.ServeEndpoint(logging.NewLogger(logging.LevelError, io.Discard), conn)
}

// maxSyncInitBytes bounds the overlay's initialization frame, as the
// server's does.
const maxSyncInitBytes = 64 << 10

// syncConn is a Sync stream as the byte stream an endpoint serves. The
// compression byte passes on its own, since the client waits for the
// endpoint's answer to it; the initialization request after it gets the
// checkout as its root.
type syncConn struct {
	stream  grpc.BidiStreamingServer[guestpb.SyncChunk, guestpb.SyncChunk]
	pending []byte
	root    string
	phase   int
	ahead   bytes.Buffer
}

func (c *syncConn) Read(p []byte) (int, error) {
	switch {
	case c.phase == 0 && len(p) > 0:
		n, err := c.read(p[:1])
		if n == 1 {
			c.phase = 1
		}
		return n, err
	case c.phase == 1:
		if err := c.pin(); err != nil {
			return 0, err
		}
		c.phase = 2
	}
	if c.ahead.Len() > 0 {
		return c.ahead.Read(p)
	}
	return c.read(p)
}

func (c *syncConn) pin() error {
	length, err := binary.ReadUvarint(byteReader{c})
	if err != nil {
		return err
	}
	if length > maxSyncInitBytes {
		return fmt.Errorf("sync initialization of %d bytes", length)
	}
	frame := make([]byte, length)
	if _, err = io.ReadFull(readerFunc(c.read), frame); err != nil {
		return err
	}
	req := &remote.InitializeSynchronizationRequest{}
	if err = proto.Unmarshal(frame, req); err != nil {
		return err
	}
	info, err := os.Stat(c.root)
	if err != nil {
		return err
	}
	// The endpoint runs as root; what it creates belongs to the session.
	st := info.Sys().(*syscall.Stat_t)
	req.Root = c.root
	if req.Configuration == nil {
		req.Configuration = &synchronization.Configuration{}
	}
	req.Configuration.DefaultOwner = "id:" + strconv.FormatUint(uint64(st.Uid), 10)
	req.Configuration.DefaultGroup = "id:" + strconv.FormatUint(uint64(st.Gid), 10)
	return encoding.EncodeProtobuf(&c.ahead, req)
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

type byteReader struct{ c *syncConn }

func (b byteReader) ReadByte() (byte, error) {
	var one [1]byte
	if _, err := io.ReadFull(readerFunc(b.c.read), one[:]); err != nil {
		return 0, err
	}
	return one[0], nil
}

func (c *syncConn) read(p []byte) (int, error) {
	for len(c.pending) == 0 {
		chunk, err := c.stream.Recv()
		if err != nil {
			return 0, err
		}
		c.pending = chunk.GetData()
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *syncConn) Write(p []byte) (int, error) {
	for sent := 0; sent < len(p); {
		n := min(len(p)-sent, guest.ObjectsChunk)
		if err := c.stream.Send(&guestpb.SyncChunk{Data: p[sent : sent+n]}); err != nil {
			return sent, err
		}
		sent += n
	}
	return len(p), nil
}

// Close ends nothing: the call ends when Sync returns.
func (c *syncConn) Close() error { return nil }
