// Package server wires the Wave 1 packages into one running Aether
// server: store -> bus -> runtime -> gitengine -> ptyhost -> scheduler ->
// sshd, all fanned out from a single data directory.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/adapter"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/profile"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/reachability"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/serverupdate"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/version"
)

// Defaults for the server configuration.
const (
	DefaultDataDir = "/var/lib/aether"
	DefaultAddr    = ":2222"
	// neutralImageRepo and standardImageRepo are the published image
	// repositories. Docker requires repository names to be lowercase.
	neutralImageRepo  = "ghcr.io/3xdevops/aether-bootstrap"
	standardImageRepo = "ghcr.io/3xdevops/aether-standard"
)

// DefaultNeutralImage and DefaultStandardImage are the published images
// matching this build. Release builds pin the images published from the
// same tag, git-describe builds pin the nearest release tag, and untagged
// builds track the latest published images.
var (
	DefaultNeutralImage  = neutralImageRepo + ":" + releaseImageTag(version.Version)
	DefaultStandardImage = standardImageRepo + ":" + releaseImageTag(version.Version)
)

var (
	describeSuffixPattern = regexp.MustCompile(`-\d+-g[0-9a-f]+(?:-dirty)?$`)
	// releaseTagPattern matches the release workflow's accepted refs
	// (push tags v*) that are also valid Docker tags; the prerelease part
	// may contain hyphens (v1.2.3-rc-1).
	releaseTagPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)
)

// releaseImageTag reduces a build version to a published image tag. The
// release workflow tags every published image with the release ref name, so
// a release version is its own image tag.
func releaseImageTag(buildVersion string) string {
	tag := describeSuffixPattern.ReplaceAllString(buildVersion, "")
	tag = strings.TrimSuffix(tag, "-dirty")
	if releaseTagPattern.MatchString(tag) {
		return tag
	}
	return "latest"
}

// Config configures a Server. The zero value serves from DefaultDataDir on
// DefaultAddr with the Docker runtime and all port forwards denied.
type Config struct {
	// DataDir is the server data directory (contract §1 layout); default
	// /var/lib/aether.
	DataDir string
	// Addr is the SSH listen address; default ":2222".
	Addr string
	// NeutralImage is the server-owned image used for workspaces whose
	// environment selects the neutral base. Empty uses DefaultNeutralImage.
	// Workspace shell requests cannot override this value.
	NeutralImage string
	// StandardImage is the published standard environment image clients
	// offer as the recommended default at workspace creation, reported by
	// server.info. Empty uses DefaultStandardImage.
	StandardImage string
	// Runtime overrides the container runtime; nil means the local Docker
	// daemon.
	Runtime runtime.Runtime
	// TailnetAutoJoin registers unknown tailnet identities as approved
	// members instead of pending ones.
	TailnetAutoJoin bool
	// TailnetRequireKey additionally requires pubkey verification on
	// tailnet connections.
	TailnetRequireKey bool
	// CoordinationDisabled turns the conflict coordination kill switch off.
	// The zero value keeps coordination enabled, which is the shipped
	// default.
	CoordinationDisabled bool
	// WhoIs overrides tailnet identity resolution; nil keeps the default
	// (the local tailscaled socket when present). The E2E suite stubs it
	// so join and fallback scenarios need no real tailnet.
	WhoIs sshd.WhoIsResolver
	// SelfUpdate overrides the server self-update service's release feed
	// and restart mechanics; the zero value is the pinned GitHub releases
	// and a real re-exec of this binary. Store and Bus are the server's
	// own and are ignored here. The E2E suite sets it so an update runs
	// against a stub release server and never replaces a real binary.
	SelfUpdate serverupdate.Config
	// Harnesses are server-owned, administrator-supplied launch definitions.
	// They are validated before the scheduler starts; ordinary workspace
	// members have no request field that can alter them.
	Harnesses map[string]scheduler.HarnessSpec

	// The failure-handling tuning knobs, all passed through to the
	// scheduler and all documented in docs/failure-handling.md. Zero means
	// the scheduler's shipped default.
	//
	// StallThreshold is how long a run may go with no PTY output and no
	// file changes before it parks at needs-attention; PollInterval is how
	// often that is checked. CheckoutTTL is how long a finished run's
	// checkout is kept before the GC reclaims it (negative disables GC).
	// MinFreeDiskBytes is the free-space floor below which new runs are
	// refused (negative disables the floor).
	StallThreshold   time.Duration
	PollInterval     time.Duration
	CheckoutTTL      time.Duration
	MinFreeDiskBytes int64
}

// Server is the assembled Aether server.
type Server struct {
	db       *store.DB
	log      *events.SQLiteLog
	bus      *events.InProc
	rt       runtime.Runtime
	docker   *runtime.Docker // set only when the server constructed it
	git      *gitengine.Engine
	pty      *ptyhost.Host
	sched    *scheduler.Scheduler
	adapters *adapter.Manager
	ssh      *sshd.Server
	services []namedService

	closeOnce sync.Once
	closeErr  error
}

// New constructs every component from cfg, fanning the data directory out
// per the Wave 1 contract's layout. The PTY write gate enforces the Wave 3
// permission model (steer capability) against the store.
func New(ctx context.Context, cfg Config) (srv *Server, err error) {
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir
	}
	if cfg.Addr == "" {
		cfg.Addr = DefaultAddr
	}
	if cfg.NeutralImage == "" {
		cfg.NeutralImage = DefaultNeutralImage
	}
	if cfg.StandardImage == "" {
		cfg.StandardImage = DefaultStandardImage
	}

	s := &Server{}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()

	if err = os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("server: create data dir: %w", err)
	}
	if s.db, err = store.Open(filepath.Join(cfg.DataDir, "aether.db")); err != nil {
		return nil, err
	}
	if s.log, err = events.OpenSQLiteLog(filepath.Join(cfg.DataDir, "aether.db")); err != nil {
		return nil, err
	}
	if s.bus, err = events.NewInProc(ctx, s.log); err != nil {
		return nil, err
	}

	s.rt = cfg.Runtime
	if s.rt == nil {
		if s.docker, err = runtime.NewDocker(); err != nil {
			return nil, err
		}
		s.rt = s.docker
	}

	if s.git, err = gitengine.New(gitengine.Config{
		ReposDir:     filepath.Join(cfg.DataDir, "repos"),
		CheckoutsDir: filepath.Join(cfg.DataDir, "checkouts"),
		Bus:          s.bus,
		OnBranchPublished: func(run domain.RunID, commit string, at time.Time) {
			if s.sched == nil {
				return
			}
			if err := s.sched.RecordCommit(context.Background(), run, commit, at); err != nil {
				slog.Warn("server: record run commit failed", "run", run, "error", err)
			}
		},
	}); err != nil {
		return nil, err
	}
	if s.pty, err = ptyhost.New(ptyhost.Config{
		TranscriptDir: filepath.Join(cfg.DataDir, "transcripts"),
		Gate:          sshd.NewWriteGate(s.db),
	}); err != nil {
		return nil, err
	}
	prof, perr := profile.New(s.db)
	if perr != nil {
		return nil, perr
	}
	homesRoot := filepath.Join(cfg.DataDir, "homes")
	homes, herr := memberhome.New(homesRoot)
	if herr != nil {
		return nil, fmt.Errorf("server: create member homes: %w", herr)
	}
	names := make([]string, 0, len(harness.Profiles()))
	for _, p := range harness.Profiles() {
		names = append(names, p.Name)
	}
	members, merr := s.db.ListMembers(ctx)
	if merr != nil {
		return nil, fmt.Errorf("server: list members for home migration: %w", merr)
	}
	for _, member := range members {
		definitions, derr := s.db.ListHarnessDefinitions(ctx, member.ID)
		if derr != nil {
			return nil, fmt.Errorf("server: list harness definitions for member %q: %w", member.ID, derr)
		}
		for _, definition := range definitions {
			names = append(names, definition.Name)
		}
	}
	if merr := memberhome.MigrateLegacyHomes(homesRoot, names); merr != nil {
		return nil, fmt.Errorf("server: migrate legacy homes: %w", merr)
	}
	if rerr := os.RemoveAll(filepath.Join(cfg.DataDir, "toolenv")); rerr != nil {
		return nil, fmt.Errorf("server: remove legacy toolenv: %w", rerr)
	}
	if s.sched, err = scheduler.New(scheduler.Config{
		Store:          s.db,
		Runtime:        s.rt,
		Bus:            s.bus,
		Git:            lazyGit{s.git},
		PTY:            s.pty,
		StateDir:       filepath.Join(cfg.DataDir, "scheduler"),
		Homes:          homes,
		ReposDir:       filepath.Join(cfg.DataDir, "repos"),
		Profiles:       prof,
		EnvEditDir:     filepath.Join(cfg.DataDir, "env-edits"),
		NeutralImage:   cfg.NeutralImage,
		Harnesses:      cfg.Harnesses,
		StallThreshold: cfg.StallThreshold,
		PollInterval:   cfg.PollInterval,
		CheckoutTTL:    cfg.CheckoutTTL,
		MinFreeBytes:   cfg.MinFreeDiskBytes,
	}); err != nil {
		return nil, err
	}
	s.adapters = adapter.NewManager(s.bus, s.db, s.pty)
	whois := cfg.WhoIs
	var tailnetHostname string
	if _, statErr := os.Stat(sshd.DefaultTailscaledSocket); whois == nil && statErr == nil {
		whois = sshd.NewLocalWhoIs("")
		// Discover the MagicDNS name once at startup; server.info reports
		// it verbatim. Best-effort: an unreachable LocalAPI leaves it empty.
		discoverCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if ep, derr := reachability.NewTailscale("").Discover(discoverCtx); derr == nil {
			tailnetHostname = ep.Host
		}
		cancel()
	}
	sshCfg := sshd.Config{
		Addr:              cfg.Addr,
		HostKeyPath:       filepath.Join(cfg.DataDir, "ssh", "host_ed25519_key"),
		Store:             s.db,
		Bus:               s.bus,
		Git:               lazyGit{s.git},
		PTY:               s.pty,
		Runs:              s.sched,
		Homes:             homes,
		WhoIs:             whois,
		TailnetAutoJoin:   cfg.TailnetAutoJoin,
		TailnetRequireKey: cfg.TailnetRequireKey,
		TailnetHostname:   tailnetHostname,
		NeutralImage:      cfg.NeutralImage,
		StandardImage:     cfg.StandardImage,
		InvitesDir:        filepath.Join(cfg.DataDir, "invites"),
		Profiles:          prof,
	}
	if err = s.buildServices(Deps{
		Config:  cfg,
		DataDir: cfg.DataDir,
		Store:   s.db,
		Bus:     s.bus,
		Events:  s.log,
		Runs:    s.sched,
		Git:     s.git,
		PTY:     s.pty,
		SSH:     &sshCfg,
	}); err != nil {
		return nil, err
	}
	if s.ssh, err = sshd.New(sshCfg); err != nil {
		return nil, err
	}
	return s, nil
}

// Store is the server's persistence layer.
func (s *Server) Store() store.Store { return s.db }

// Bus is the server's event bus.
func (s *Server) Bus() events.Bus { return s.bus }

// SSHAddr returns the bound SSH listen address, or nil before Run has
// bound it.
func (s *Server) SSHAddr() net.Addr { return s.ssh.Addr() }

// Run ensures every workspace has its bare repo, then serves SSH and runs
// the scheduler until ctx is done or one of them fails, and finally shuts
// everything down in dependency order. Run owns shutdown on every path:
// it closes the server before returning even when startup fails.
func (s *Server) Run(ctx context.Context) error {
	workspaces, err := s.db.ListWorkspaces(ctx)
	if err != nil {
		return errors.Join(err, s.Close())
	}
	for _, ws := range workspaces {
		if _, err := s.git.InitWorkspaceRepo(ctx, ws.ID); err != nil {
			return errors.Join(fmt.Errorf("server: init repo for workspace %s: %w", ws.ID, err), s.Close())
		}
	}
	if err := s.adapters.Start(ctx); err != nil {
		return errors.Join(fmt.Errorf("server: adapter manager: %w", err), s.Close())
	}
	if err := s.startServices(ctx); err != nil {
		return errors.Join(err, s.Close())
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := s.sched.Start(runCtx); err != nil {
			errc <- fmt.Errorf("server: scheduler: %w", err)
			cancel()
		}
	}()
	go func() {
		defer wg.Done()
		if err := s.ssh.Serve(runCtx); err != nil {
			errc <- fmt.Errorf("server: sshd: %w", err)
			cancel()
		}
	}()
	<-runCtx.Done()
	wg.Wait()

	closeErr := s.Close()
	select {
	case err := <-errc:
		return errors.Join(err, closeErr)
	default:
	}
	return closeErr
}

// Close shuts the components down in dependency order: SSH transport
// first (no new work arrives), then the scheduler (supervision loops
// stop; containers keep running), then the PTY host (transcripts flush),
// the git engine (diff watchers stop), and finally the bus, event log,
// runtime, and store. Idempotent.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		var errs []error
		closeAll := []func() error{}
		if s.ssh != nil {
			closeAll = append(closeAll, s.ssh.Close)
		}
		if s.sched != nil {
			closeAll = append(closeAll, s.sched.Close)
		}
		if s.adapters != nil {
			closeAll = append(closeAll, s.adapters.Close)
		}
		// Registered services publish events, so they stop before the bus.
		closeAll = append(closeAll, s.closeServices()...)
		if s.pty != nil {
			closeAll = append(closeAll, s.pty.Close)
		}
		if s.git != nil {
			closeAll = append(closeAll, s.git.Close)
		}
		if s.bus != nil {
			closeAll = append(closeAll, s.bus.Close)
		}
		if s.log != nil {
			closeAll = append(closeAll, s.log.Close)
		}
		if s.docker != nil {
			closeAll = append(closeAll, s.docker.Close)
		}
		if s.db != nil {
			closeAll = append(closeAll, s.db.Close)
		}
		for _, fn := range closeAll {
			if err := fn(); err != nil {
				errs = append(errs, err)
			}
		}
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}
