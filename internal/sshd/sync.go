package sshd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mutagen-io/mutagen/pkg/encoding"
	"github.com/mutagen-io/mutagen/pkg/logging"
	"github.com/mutagen-io/mutagen/pkg/synchronization"
	"github.com/mutagen-io/mutagen/pkg/synchronization/compression"
	"github.com/mutagen-io/mutagen/pkg/synchronization/core"
	"github.com/mutagen-io/mutagen/pkg/synchronization/core/ignore"
	"github.com/mutagen-io/mutagen/pkg/synchronization/endpoint/remote"
	"golang.org/x/crypto/ssh"
	"google.golang.org/protobuf/proto"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	// sync.conflict is Steer-gated like the bridge itself: only a member
	// who could have opened the overlay may publish its conflict event.
	registerGuarded(protocol.MethodSyncConflict, permissions.Steer, runTarget, (*Server).syncConflict)
}

const (
	// maxSyncHeaderBytes bounds the JSON header line. The request is a run
	// ID and a flag; the shared protocol.MaxLineBytes cap exists for
	// profile pushes and would let one channel buffer 32 MiB here.
	maxSyncHeaderBytes = 4 << 10
	// maxSyncInitBytes bounds the endpoint initialization frame. Mutagen's
	// generic decoder allocates for any length prefix up to 100 MiB before
	// reading a byte of it (pkg/encoding/protobuf.go); the real init
	// message is well under a kilobyte.
	maxSyncInitBytes = 64 << 10
	// maxSyncChannelsPerMember caps a member's concurrent aether-sync
	// channels. One live overlay per run plus headroom for the old and new
	// stream overlapping during a mutagen reconnect.
	maxSyncChannelsPerMember = 8
	// defaultSyncHandshakeTimeout bounds everything before the endpoint is
	// serving: header line, ack, compression byte, and init frame. The
	// post-auth SSH deadline is cleared (see handleConn), so without this
	// an authenticated member can hold channels open indefinitely by
	// drip-feeding the setup.
	defaultSyncHandshakeTimeout = 20 * time.Second
	// defaultSyncRevalidateInterval is how often an authorized,
	// already-serving bridge re-checks that it is still authorized.
	defaultSyncRevalidateInterval = 3 * time.Second
)

var (
	errSyncInitTooLarge = errors.New("sshd: sync init frame exceeds the allowed size")
	errSyncHeaderLarge  = errors.New("sshd: sync header exceeds the allowed size")
	errSyncRevoked      = errors.New("sshd: sync authorization revoked")
)

// serveSync wires an aether-sync subsystem channel to a mutagen remote
// endpoint rooted at the run's worktree: one SyncRequest header line in,
// an ack, then the raw remaining stream is the mutagen endpoint protocol.
// The git backbone never depends on this bridge; it only reads the
// worktree path from the run row.
//
// Write access to a worktree is the Steer capability - the same policy as
// writing to the run's terminal (see NewWriteGate): a member who may not
// steer a run must not be able to edit its files either. The actor and
// target are re-fetched per bridge so demotions and protection flips
// apply to new overlays immediately, and re-fetched again on a timer
// while the endpoint is being served so they apply to live ones too.
//
// The client is treated as hostile throughout. It chooses none of what
// the endpoint touches on disk: rootPinningStream.pin overwrites the
// root, the session identifier, the endpoint role, and every
// security-relevant configuration field before mutagen decodes the
// initialization frame.
//
// Mid-write refusal: the bridge rejects setup while the run status is
// `running` unless the client forces. Run status is the chosen signal
// because it is the cheapest reliable server-side indicator that the
// agent may be actively writing the worktree; diff/PTY-output recency was
// considered and rejected as racy (a quiet agent mid-edit defeats it, and
// it turns a deterministic check into a timing-dependent one). It is a
// setup-time courtesy rather than a permission, so it is not re-checked.
func (s *Server) serveSync(ctx context.Context, member domain.MemberID, ch ssh.Channel) {
	defer func() { _ = ch.Close() }()

	// Closing the channel is what unblocks the reads below, so the whole
	// handshake runs under one timer.
	handshake := time.AfterFunc(s.cfg.syncHandshakeTimeout, func() { _ = ch.Close() })
	defer handshake.Stop()

	// The header cap is lifted once the header is parsed: the same
	// buffered reader goes on to carry the endpoint stream, whose frames
	// are bounded separately.
	capped := &capReader{r: ch, left: maxSyncHeaderBytes}
	r := bufio.NewReaderSize(capped, 16<<10)
	line, err := protocol.ReadLine(r)
	if err != nil {
		return
	}
	capped.left = -1

	var req protocol.SyncRequest
	if uerr := json.Unmarshal(line, &req); uerr != nil {
		_ = writeJSONLine(ch, protocol.SyncResponse{OK: false, Code: protocol.CodeParse, Error: "parse error: " + uerr.Error()})
		return
	}
	if req.RunID == "" {
		_ = writeJSONLine(ch, protocol.SyncResponse{OK: false, Code: protocol.CodeInvalidParams, Error: "run_id is required"})
		return
	}
	if merr := s.checkMember(ctx, member); merr != nil {
		e := rpcError(merr)
		_ = writeJSONLine(ch, protocol.SyncResponse{OK: false, Code: e.Code, Error: e.Message})
		return
	}
	// Claimed before the remaining store work so a member cannot fan out
	// channels to multiply it; released as soon as this handler returns,
	// so refusals below cost nothing.
	if !s.claimSyncChannel(member) {
		_ = writeJSONLine(ch, protocol.SyncResponse{OK: false, Code: protocol.CodeConflict, Error: "too many concurrent sync overlays for this member"})
		return
	}
	defer s.releaseSyncChannel(member)

	run, err := s.cfg.Store.GetRun(ctx, domain.RunID(req.RunID))
	if err != nil {
		e := rpcError(err)
		_ = writeJSONLine(ch, protocol.SyncResponse{OK: false, Code: e.Code, Error: e.Message})
		return
	}
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		e := rpcError(err)
		_ = writeJSONLine(ch, protocol.SyncResponse{OK: false, Code: e.Code, Error: e.Message})
		return
	}
	target, err := resolveRunTarget(ctx, s.cfg.Store, run.ID)
	if err != nil {
		e := rpcError(err)
		_ = writeJSONLine(ch, protocol.SyncResponse{OK: false, Code: e.Code, Error: e.Message})
		return
	}
	if cerr := permissions.Check(permissions.Steer, actor, target); cerr != nil {
		_ = writeJSONLine(ch, protocol.SyncResponse{OK: false, Code: protocol.CodeDenied, Error: cerr.Error()})
		return
	}
	if run.Status.Terminal() {
		_ = writeJSONLine(ch, protocol.SyncResponse{OK: false, Code: protocol.CodeInvalidState, Error: "run is " + string(run.Status)})
		return
	}
	if run.Worktree == "" {
		_ = writeJSONLine(ch, protocol.SyncResponse{OK: false, Code: protocol.CodeUnavailable, Error: "run has no worktree"})
		return
	}
	if run.Status == domain.RunRunning && !req.Force {
		_ = writeJSONLine(ch, protocol.SyncResponse{OK: false, Code: protocol.CodeInvalidState, Error: "run is running; the agent may be mid-write (use --force to overlay anyway)"})
		return
	}
	session, serr := syncSessionID(run.ID)
	if serr != nil {
		_ = writeJSONLine(ch, protocol.SyncResponse{OK: false, Code: protocol.CodeInternal, Error: serr.Error()})
		return
	}
	if werr := writeJSONLine(ch, protocol.SyncResponse{OK: true}); werr != nil {
		return
	}

	// The compression handshake's algorithm byte arrives next. Only the
	// uncompressed protocol is bridged: pinning below rewrites the
	// endpoint initialization frame, which is only possible when the
	// stream is plain uvarint-framed protobuf. 0 is ClientHandshake's
	// "algorithm unsupported" reply.
	algo, err := r.ReadByte()
	if err != nil {
		return
	}
	if compression.Algorithm(algo) != compression.Algorithm_AlgorithmNone {
		_, _ = ch.Write([]byte{0})
		return
	}

	// Hand the remaining raw stream to mutagen. ctx is canceled when the
	// session channel closes (handleSession's request loop ending), which
	// closes the channel and unblocks ServeEndpoint's reads.
	stop := context.AfterFunc(ctx, func() { _ = ch.Close() })
	defer stop()
	stream := &rootPinningStream{
		r:       r,
		ch:      ch,
		algo:    algo,
		root:    run.Worktree,
		session: session,
		onInit:  func() { handshake.Stop() },
	}
	authCtx, stopAuth := context.WithCancel(ctx)
	defer stopAuth()
	s.spawn(func() { s.revokeSyncOnPolicyChange(authCtx, member, run.ID, run.Worktree, stream) })

	if serr := remote.ServeEndpoint(logging.NewLogger(logging.LevelError, io.Discard), stream); serr != nil {
		sendExitStatus(ch, 1)
		return
	}
	sendExitStatus(ch, 0)
}

// syncSessionID derives the mutagen session identifier the endpoint is
// served under. It must be server-chosen: mutagen only checks that the
// identifier is non-empty and then filepath.Joins it into the endpoint's
// cache path and staging root (endpoint/local/paths.go), both of which it
// then writes to, so a client value of "../.." writes wherever it likes.
// Deriving it from the run keeps it stable across reconnects - an
// interrupted staging resumes instead of leaking a directory per dropped
// connection - and the run ID's alphabet is checked rather than
// sanitized, so nothing that is not plainly one path element can reach
// mutagen even if run IDs ever change shape.
func syncSessionID(run domain.RunID) (string, error) {
	invalid := errors.New("run id is not usable as a sync session identifier")
	if len(run) == 0 || len(run) > 64 {
		return "", invalid
	}
	for i := range len(run) {
		switch c := run[i]; {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-', c == '_':
		default:
			return "", invalid
		}
	}
	return "aether-" + string(run), nil
}

// capReader bounds how many bytes may be consumed from the channel until
// the cap is lifted by setting left negative.
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left == 0 {
		return 0, errSyncHeaderLarge
	}
	if c.left > 0 && int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	if c.left > 0 {
		c.left -= int64(n)
	}
	return n, err
}

// rootPinningStream is the SSH channel as the io.ReadWriteCloser mutagen
// owns after the ack. It forces the served endpoint's behaviour by
// intercepting the client-to-server preamble: the already-vetted
// compression algorithm byte is replayed, then the
// InitializeSynchronizationRequest frame is decoded under a size cap,
// overwritten by pin, and re-encoded before ServeEndpoint decodes it.
// Everything after the init frame passes through verbatim - with
// compression "none" both directions are plain uvarint-framed protobuf,
// so no further transformation is needed. Writes (the handshake reply and
// all responses) pass through untouched.
//
// Both directions fail closed once revoked, so no worktree byte is read
// or written after authorization is lost even if the peer ignores the
// channel close that accompanies it.
type rootPinningStream struct {
	r       *bufio.Reader
	ch      ssh.Channel
	root    string
	session string
	// onInit reports that the init frame has been consumed, which ends
	// the handshake deadline.
	onInit func()

	// phase is the read-side interception state: 0 replays algo, 1
	// rewrites the init frame, 2 delegates to the raw stream.
	phase   int
	algo    byte
	buf     bytes.Buffer
	err     error
	revoked atomic.Bool
}

func (s *rootPinningStream) Read(p []byte) (int, error) {
	if s.revoked.Load() {
		return 0, errSyncRevoked
	}
	if s.err != nil {
		return 0, s.err
	}
	for s.buf.Len() == 0 {
		switch s.phase {
		case 0:
			s.buf.WriteByte(s.algo)
			s.phase = 1
		case 1:
			// Blocks until the client, having seen ServeEndpoint's
			// handshake reply (written through this stream in the
			// meantime), sends its initialization request.
			req, derr := s.readInit()
			if derr != nil {
				s.err = derr
				return 0, derr
			}
			s.pin(req)
			if err := encoding.EncodeProtobuf(&s.buf, req); err != nil {
				s.err = err
				return 0, err
			}
			s.phase = 2
			if s.onInit != nil {
				s.onInit()
			}
		default:
			return s.r.Read(p)
		}
	}
	return s.buf.Read(p)
}

func (s *rootPinningStream) Write(p []byte) (int, error) {
	if s.revoked.Load() {
		return 0, errSyncRevoked
	}
	return s.ch.Write(p)
}

func (s *rootPinningStream) Close() error { return s.ch.Close() }

// revoke fails the stream closed and drops the channel, which unwinds
// ServeEndpoint and its endpoint goroutines. Idempotent.
func (s *rootPinningStream) revoke() {
	if s.revoked.CompareAndSwap(false, true) {
		_ = s.ch.Close()
	}
}

// readInit decodes the initialization frame with its own length check.
// Mutagen's decoder sizes its buffer from the length prefix before
// reading any of the message, so an authenticated member could otherwise
// force a 100 MiB allocation per channel and then never send the body.
func (s *rootPinningStream) readInit() (*remote.InitializeSynchronizationRequest, error) {
	length, err := binary.ReadUvarint(s.r)
	if err != nil {
		return nil, err
	}
	if length > maxSyncInitBytes {
		return nil, errSyncInitTooLarge
	}
	frame := make([]byte, length)
	if _, err := io.ReadFull(s.r, frame); err != nil {
		return nil, err
	}
	req := &remote.InitializeSynchronizationRequest{}
	if err := proto.Unmarshal(frame, req); err != nil {
		return nil, err
	}
	return req, nil
}

// pin overwrites every field of the initialization request that decides
// what the endpoint touches on disk. The client keeps only what it cannot
// escape with: the ignore list and its syntax are subtractive, so they
// can shrink the synchronized set but never grow it, and they must match
// the client's own alpha endpoint for the session to be healthy.
//
// Server-controlled, and why:
//
//	Root                 the run's worktree; a client root is an
//	                     arbitrary-path read/write hole
//	Session              names the cache file and staging root, both
//	                     built by joining it onto a directory
//	Alpha                the worktree is always the beta side; alpha
//	                     selects different cache and staging names
//	Version              the vendored session version, whose defaults are
//	                     what every field left zero below resolves to
//	IgnoreVCSMode        Ignore, so .git is never scanned, overwritten,
//	                     or deleted by a sync; mutagen's own default for
//	                     this field is Propagate
//	SymbolicLinkMode     Portable, so a symlink pointing out of the
//	                     worktree is never created or followed
//	StageMode            Mutagen, so staging never lands beside the
//	                     worktree (Neighboring) or inside it (Internal)
//	SynchronizationMode  two-way-safe, so conflicts pause instead of one
//	                     side being silently clobbered
//	PermissionsMode      Portable, with the default file mode, directory
//	                     mode, owner, and group left zero: a client value
//	                     would set ownership and permissions on files the
//	                     server creates
//	CompressionAlgorithm none, because pinning requires the plain
//	                     uvarint-framed protocol
//	everything else      zero, i.e. the session version's default
func (s *rootPinningStream) pin(req *remote.InitializeSynchronizationRequest) {
	ignores, syntax := req.Configuration.GetIgnores(), req.Configuration.GetIgnoreSyntax()
	req.Root = s.root
	req.Session = s.session
	req.Alpha = false
	req.Version = synchronization.DefaultVersion
	req.Configuration = &synchronization.Configuration{
		SynchronizationMode:  core.SynchronizationMode_SynchronizationModeTwoWaySafe,
		SymbolicLinkMode:     core.SymbolicLinkMode_SymbolicLinkModePortable,
		IgnoreSyntax:         syntax,
		Ignores:              ignores,
		IgnoreVCSMode:        ignore.IgnoreVCSMode_IgnoreVCSModeIgnore,
		StageMode:            synchronization.StageMode_StageModeMutagen,
		PermissionsMode:      core.PermissionsMode_PermissionsModePortable,
		CompressionAlgorithm: compression.Algorithm_AlgorithmNone,
	}
}

// revokeSyncOnPolicyChange re-runs the bridge's authorization while the
// endpoint is being served. The pre-ack checks are a snapshot: without
// this, a member demoted, removed, or handed off mid-stream keeps write
// access to the worktree for as long as it holds the channel open, and so
// does a client whose run has since been protected, restricted by
// steer_others, or reached a terminal state. The client-side status
// watcher in `aether sync` is a courtesy, not a control - a hostile
// client simply omits it.
func (s *Server) revokeSyncOnPolicyChange(ctx context.Context, member domain.MemberID, run domain.RunID, worktree string, stream *rootPinningStream) {
	ticker := time.NewTicker(s.cfg.syncRevalidateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.syncStillAuthorized(ctx, member, run, worktree) {
				continue
			}
			stream.revoke()
			return
		}
	}
}

// syncStillAuthorized repeats every gate serveSync applied before the ack
// that is a permission rather than a courtesy. Store reads only: this
// runs every few seconds per live overlay.
func (s *Server) syncStillAuthorized(ctx context.Context, member domain.MemberID, id domain.RunID, worktree string) bool {
	if s.checkMember(ctx, member) != nil {
		return false
	}
	run, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil || run.Status.Terminal() || run.Worktree != worktree {
		return false
	}
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return false
	}
	target, err := resolveRunTarget(ctx, s.cfg.Store, id)
	if err != nil {
		return false
	}
	return permissions.Check(permissions.Steer, actor, target) == nil
}

// claimSyncChannel reserves one of the member's concurrent aether-sync
// slots, reporting false when it is already at the cap. Each channel owns
// a mutagen endpoint with its own watcher and staging goroutines, so an
// uncapped member could multiply that work by opening channels.
func (s *Server) claimSyncChannel(member domain.MemberID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.syncChannels[member] >= maxSyncChannelsPerMember {
		return false
	}
	s.syncChannels[member]++
	return true
}

func (s *Server) releaseSyncChannel(member domain.MemberID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.syncChannels[member] - 1; n > 0 {
		s.syncChannels[member] = n
	} else {
		delete(s.syncChannels, member)
	}
}

const (
	// maxConflictFiles and maxConflictPathBytes bound the client-reported
	// conflict list; past either the report is not a conflict report.
	maxConflictFiles     = 256
	maxConflictPathBytes = 512
	// maxSyncSessionIDBytes bounds the reported mutagen session ID.
	maxSyncSessionIDBytes = 128
)

// syncConflict publishes the sync.conflict event on behalf of a paused
// live overlay: the mutagen session runs client-side, so conflict
// detection happens there, but events are only ever published by the
// server. Members carries the syncing member and the run owner,
// deduplicated when they are the same person.
//
// Nothing in the params was produced by the server, and the event reaches
// every subscriber of the run's session, so the payload is bounded to
// what a conflict report can plausibly be: plain relative paths, free of
// control characters that would otherwise reach a subscriber's terminal
// verbatim. Within those bounds it exposes no more than comparable run
// events already do - run.diff carries worktree paths and member.list
// carries member IDs.
func (s *Server) syncConflict(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.SyncConflictParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.RunID == "" || len(p.Files) == 0 {
		return nil, invalidParams("run_id and files are required")
	}
	if len(p.Files) > maxConflictFiles {
		return nil, invalidParams("too many conflicted files reported")
	}
	for _, f := range p.Files {
		if !relativeSyncPath(f) {
			return nil, invalidParams("files must be plain paths relative to the sync root")
		}
	}
	if len(p.SyncSessionID) > maxSyncSessionIDBytes || hasControlChars(p.SyncSessionID) {
		return nil, invalidParams("sync_session_id is not a valid identifier")
	}
	run, err := s.cfg.Store.GetRun(ctx, domain.RunID(p.RunID))
	if err != nil {
		return nil, rpcError(err)
	}
	members := []domain.MemberID{member}
	if run.MemberID != member {
		members = append(members, run.MemberID)
	}
	if _, err := s.cfg.Bus.Publish(ctx, events.Event{
		SessionID: run.SessionID,
		RunID:     run.ID,
		ActorID:   member,
		Payload: events.SyncConflictPayload{
			RunID:         run.ID,
			SyncSessionID: p.SyncSessionID,
			Files:         p.Files,
			Members:       members,
		},
	}); err != nil {
		return nil, rpcError(err)
	}
	// The typed event is only seen by a client that asked for its type,
	// and the one client known to be watching this run (`aether sync`)
	// filters for run.status. The run owner would otherwise never learn
	// that their worktree is half-synced. The session timeline is this
	// repo's stream for a noteworthy act on someone else's run - handoff,
	// protection, and steer_others all stamp one - so the notice rides
	// there, where members are already looking. It carries no path names:
	// those stay in the typed event for a client that wants them.
	_, _ = s.cfg.Bus.Publish(ctx, events.Event{
		SessionID: run.SessionID,
		RunID:     run.ID,
		ActorID:   member,
		Payload: events.TimelinePayload{
			Kind:    events.TimelineNote,
			Message: "live overlay paused on " + strconv.Itoa(len(p.Files)) + " sync conflict(s); the run worktree is canonical",
		},
	})
	return struct{}{}, nil
}

// relativeSyncPath reports whether p is a plain path inside a sync root:
// bounded, not absolute, and without empty, "." or ".." segments.
func relativeSyncPath(p string) bool {
	if p == "" || len(p) > maxConflictPathBytes || p[0] == '/' {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return !hasControlChars(p)
}

func hasControlChars(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}
