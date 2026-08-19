// Package overlay is the client half of the live file overlay (`aether
// sync --live`): it embeds the mutagen synchronization engine - no
// daemon - to mirror one local directory against one run worktree over
// the aether-sync SSH subsystem. The server side (internal/sshd) only
// serves a mutagen remote endpoint rooted at the worktree; all session
// state lives client-side in a per-invocation temp data directory, so
// every invocation starts fresh and "resume after conflict" is simply
// rerunning the command.
//
// The git backbone never depends on any of this: the overlay ignores VCS
// directories, so .git and the bare repo are never touched.
package overlay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mutagen-io/mutagen/pkg/logging"
	"github.com/mutagen-io/mutagen/pkg/selection"
	"github.com/mutagen-io/mutagen/pkg/synchronization"
	"github.com/mutagen-io/mutagen/pkg/synchronization/compression"
	"github.com/mutagen-io/mutagen/pkg/synchronization/core"
	"github.com/mutagen-io/mutagen/pkg/synchronization/core/ignore"
	"github.com/mutagen-io/mutagen/pkg/synchronization/endpoint/remote"
	"github.com/mutagen-io/mutagen/pkg/url"

	// The alpha (local directory) endpoint connects through mutagen's
	// local protocol handler, registered by this import's init(). The
	// beta handler is ours (SSH slot); mutagen's own ssh/docker handler
	// packages are never imported.
	_ "github.com/mutagen-io/mutagen/pkg/synchronization/protocols/local"
)

// Dialer opens one aether-sync stream to the server. Each mutagen
// connection attempt gets a fresh stream (transient channel drops
// reconnect through it); returning an error surfaces as the session's
// connection error.
type Dialer func(ctx context.Context) (io.ReadWriteCloser, error)

// Options configures a live overlay session.
type Options struct {
	// LocalDir is the path of the local directory (alpha); made
	// absolute internally.
	LocalDir string
	// Dial opens the SSH subsystem stream to the run worktree (beta).
	Dial Dialer
	// DataDir is the mutagen data directory for this invocation. Empty
	// means a fresh temp dir, removed on Close.
	DataDir string
}

// Session is one live overlay: an embedded mutagen manager owning
// exactly one synchronization session between LocalDir and the dialed
// run worktree.
type Session struct {
	localDir   string
	manager    *synchronization.Manager
	sessionID  string
	dataDir    string
	ownsData   bool
	stateIndex uint64
}

// overlayHost is the fake hostname carried in the beta URL; the run ID
// travels in the URL path. It squats mutagen's SSH protocol slot: the
// URL protocol enum is closed (local/ssh/docker), mutagen's own ssh
// handler package is never imported, and "aether-run" is never a real
// hostname, so there is no collision with genuine SSH URLs.
const overlayHost = "aether-run"

// streamProtocolHandler adapts a Dialer to mutagen's ProtocolHandler
// registry.
type streamProtocolHandler struct {
	dial Dialer
}

func (h *streamProtocolHandler) Connect(
	ctx context.Context,
	logger *logging.Logger,
	u *url.URL,
	_ string,
	session string,
	version synchronization.Version,
	configuration *synchronization.Configuration,
	alpha bool,
) (synchronization.Endpoint, error) {
	stream, err := h.dial(ctx)
	if err != nil {
		return nil, err
	}
	// The root sent here is advisory only: the server bridge pins the
	// endpoint root to the run's worktree no matter what travels in the
	// init frame.
	return remote.NewEndpoint(logger, stream, u.Path, session, version, configuration, alpha)
}

// NewSession creates (but does not start) the overlay: it prepares the
// data directory, registers the protocol handler, and builds the
// manager. Exactly one Session per process: the handler registry and
// MUTAGEN_DATA_DIRECTORY are process-global.
func NewSession(opts Options) (*Session, error) {
	if opts.LocalDir == "" || opts.Dial == nil {
		return nil, errors.New("overlay: LocalDir and Dial are required")
	}
	localDir, err := filepath.Abs(opts.LocalDir)
	if err != nil {
		return nil, fmt.Errorf("overlay: resolve local dir: %w", err)
	}
	if info, serr := os.Stat(localDir); serr != nil {
		return nil, fmt.Errorf("overlay: local dir: %w", serr)
	} else if !info.IsDir() {
		return nil, fmt.Errorf("overlay: %s is not a directory", localDir)
	}
	dataDir, ownsData := opts.DataDir, false
	if dataDir == "" {
		if dataDir, err = os.MkdirTemp("", "aether-overlay-*"); err != nil {
			return nil, fmt.Errorf("overlay: data dir: %w", err)
		}
		ownsData = true
	}
	// Mutagen resolves its data directory through this env var on every
	// path computation; per-invocation isolation keeps `aether sync`
	// stateless.
	if err = os.Setenv("MUTAGEN_DATA_DIRECTORY", dataDir); err != nil {
		if ownsData {
			_ = os.RemoveAll(dataDir)
		}
		return nil, fmt.Errorf("overlay: set data dir: %w", err)
	}
	synchronization.ProtocolHandlers[url.Protocol_SSH] = &streamProtocolHandler{dial: opts.Dial}
	manager, err := synchronization.NewManager(logging.NewLogger(logging.LevelError, io.Discard))
	if err != nil {
		if ownsData {
			_ = os.RemoveAll(dataDir)
		}
		return nil, fmt.Errorf("overlay: manager: %w", err)
	}
	return &Session{localDir: localDir, manager: manager, dataDir: dataDir, ownsData: ownsData}, nil
}

// Start creates and connects the mutagen session: alpha is the local
// directory, beta the dialed run worktree. Two-way-safe mode keeps real
// conflicts unresolved for the pause-and-preserve policy; VCS ignore
// keeps .git out of the overlay; conflict twins are ignored so preserved
// files never propagate; compression is pinned to "none" because the
// server bridge rewrites the endpoint init frame and only passes the
// uncompressed protocol.
func (s *Session) Start(ctx context.Context, runID string) error {
	alpha := &url.URL{
		Kind:     url.Kind_Synchronization,
		Protocol: url.Protocol_Local,
		Path:     s.localDir,
	}
	beta := &url.URL{
		Kind:     url.Kind_Synchronization,
		Protocol: url.Protocol_SSH,
		Host:     overlayHost,
		Path:     "/" + runID,
	}
	cfg := &synchronization.Configuration{
		SynchronizationMode:  core.SynchronizationMode_SynchronizationModeTwoWaySafe,
		IgnoreVCSMode:        ignore.IgnoreVCSMode_IgnoreVCSModeIgnore,
		CompressionAlgorithm: compression.Algorithm_AlgorithmNone,
		Ignores:              []string{"*" + ConflictSuffix},
	}
	none := &synchronization.Configuration{}
	id, err := s.manager.Create(ctx, alpha, beta, cfg, none, none, "aether-overlay", nil, false, "")
	if err != nil {
		return fmt.Errorf("overlay: create session: %w", err)
	}
	s.sessionID = id
	return nil
}

// LocalDir returns the absolute alpha root.
func (s *Session) LocalDir() string { return s.localDir }

// SessionID returns the mutagen session identifier (empty before Start).
func (s *Session) SessionID() string { return s.sessionID }

// Close tears the overlay down: it halts the mutagen session (clean
// endpoint shutdown on both sides) and removes owned state. Safe to call
// multiple times.
func (s *Session) Close() {
	if s.manager != nil {
		s.manager.Shutdown()
		s.manager = nil
	}
	if s.ownsData && s.dataDir != "" {
		_ = os.RemoveAll(s.dataDir)
		s.dataDir = ""
	}
}

// selection addresses this session in manager calls.
func (s *Session) selection() *selection.Selection {
	return &selection.Selection{Specifications: []string{s.sessionID}}
}
