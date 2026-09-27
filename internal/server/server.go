// Package server wires the Wave 1 packages into one running Aether
// server: store -> bus -> runtime -> gitengine -> ptyhost -> scheduler ->
// sshd, all fanned out from a single data directory.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/adapter"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/edgeagent"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/integration"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/profile"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/reachability"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/servergw"
	"github.com/3xDevOps/Aether/internal/serverupdate"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/version"
)

// Defaults for the server configuration.
const (
	DefaultDataDir = "/var/lib/aether"
	DefaultAddr    = ":2222"
	// standardImageRepo is the published image repository. Docker requires
	// repository names to be lowercase.
	standardImageRepo = "ghcr.io/3xdevops/aether-standard"
)

// DefaultStandardImage is the published image matching this build.
var DefaultStandardImage = standardImageRepo + ":" + releaseImageTag(version.Version)

var describeSuffixPattern = regexp.MustCompile(`-\d+-g[0-9a-f]+(?:-dirty)?$`)

// releaseImageTag reduces a build version to a published image tag. The
// release workflow tags every published image with the release ref name, so
// a release version is its own image tag.
func releaseImageTag(buildVersion string) string {
	tag := describeSuffixPattern.ReplaceAllString(buildVersion, "")
	tag = strings.TrimSuffix(tag, "-dirty")
	if scheduler.ReleaseTag(tag) {
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
	// WebPort serves the dashboard over HTTPS on the server's tailnet
	// addresses at this port; 0 leaves the server SSH-only. It needs
	// tailscaled on this host with MagicDNS and HTTPS certificates
	// enabled for the tailnet, and refuses to start without them.
	WebPort int
	// Runtime overrides the Docker runtime, primarily for tests.
	Runtime runtime.Runtime
	// IntegrationAdmission is the server-controlled policy seam for
	// consequential candidate mutations. It is never populated from a
	// request field.
	IntegrationAdmission integration.AdmissionFunc
	// StandardImage is the server-owned image used for all runs until member
	// image selection is available. Empty uses DefaultStandardImage.
	StandardImage string
	// BrowserImage is the pinned, sandboxed headless companion image.
	BrowserImage string
	// TailnetAutoJoin registers unknown tailnet identities as approved
	// members instead of pending ones.
	TailnetAutoJoin bool
	// TailnetRequireKey additionally requires pubkey verification on
	// tailnet connections.
	TailnetRequireKey bool
	// EdgeURL is the edge the server enrolls with (docs/edge.md); empty
	// keeps the server off every edge.
	EdgeURL string
	// EdgeDeviceAutoApprove accepts every new edge device of a member. The
	// zero value holds a member's second and later devices pending until a
	// device the member already uses, an admin, or the machine's
	// administrator approves them.
	EdgeDeviceAutoApprove bool
	// EdgeACMEDirectory is the ACME directory the edge dashboard's
	// certificate is issued from; empty is Let's Encrypt.
	EdgeACMEDirectory string
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
	// ServerBinary overrides the binary staged into run containers to
	// serve the MCP bridge; empty stages this running server
	// (scheduler.DefaultServerBinary). The E2E suite points it at a
	// binary it built, because under `go test` /proc/self/exe is the test
	// binary and has no mcp subcommand.
	ServerBinary string

	// The failure-handling tuning knobs, all passed through to the
	// scheduler and all documented in docs/failure-handling.md. Zero means
	// the scheduler's shipped default.
	//
	// StallThreshold is how long a run may go with no agent output and no
	// file changes before it parks at needs-attention; PollInterval is how
	// often that is checked. CheckoutTTL is how long a finished run's
	// checkout is kept before the GC reclaims it (negative disables GC).
	// RunContainerTTL is how long an explicitly closed TUI run's container
	// is retained for reopening (negative disables retention).
	// MinFreeDiskBytes is the free-space floor below which new runs are
	// refused (negative disables the floor).
	StallThreshold   time.Duration
	PollInterval     time.Duration
	CheckoutTTL      time.Duration
	RunContainerTTL  time.Duration
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
	control  *control.Service
	evidence *evidence.Service
	adapters *adapter.Manager
	ssh      *sshd.Server
	edge     *edgeagent.Agent
	// edgeWeb configures the dashboard gateway through the edge. Run
	// builds it once the agent has learned the edge's server domain.
	edgeWeb  servergw.EdgeConfig
	web      *servergw.Gateway
	tailnet  servergw.Tailnet
	services []namedService

	closeOnce sync.Once
	closeErr  error
}

type runTitleSetter interface {
	SetRunTitle(domain.RunID, string)
}

// runSteerRecorder records a member steering a run by typing into it. Who
// typed is known only at the attach, so the PTY host hands it back here.
type runSteerRecorder interface {
	RecordSteer(ctx context.Context, run domain.RunID, member domain.MemberID)
}

// A shell tab inside the run container is not the agent's terminal, so
// Run - which is false for one - is the right question here: opening a
// shell in someone else's run is not steering their agent.
func forwardRunSteer(rec runSteerRecorder, key ptyhost.SessionKey, member domain.MemberID) {
	run, ok := key.Run()
	if !ok || rec == nil {
		return
	}
	rec.RecordSteer(context.Background(), run, member)
}

func forwardRunTitle(setter runTitleSetter, key ptyhost.SessionKey, title string) {
	run, ok := key.Run()
	if !ok || setter == nil {
		return
	}
	setter.SetRunTitle(run, title)
}

// ptyTranscript adapts the PTY host's complete replay seam to the evidence
// service's context-free transcript exporter. The byte count belongs to
// attach framing, not the bounded evidence artifact copy.
type ptyTranscript struct{ host *ptyhost.Host }

func (p ptyTranscript) Replay(run domain.RunID) (io.ReadCloser, error) {
	reader, _, err := p.host.Replay(run)
	return reader, err
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
	if cfg.StandardImage == "" {
		cfg.StandardImage = DefaultStandardImage
	}
	if cfg.BrowserImage == "" {
		cfg.BrowserImage = DefaultBrowserImage
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
	if s.db, err = store.Open(StorePath(cfg.DataDir)); err != nil {
		return nil, err
	}
	if s.log, err = events.OpenSQLiteLog(StorePath(cfg.DataDir)); err != nil {
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
			if recordErr := s.sched.RecordCommit(context.Background(), run, commit, at); recordErr != nil {
				slog.Warn("server: record run commit failed", "run", run, "error", recordErr)
			}
		},
	}); err != nil {
		return nil, err
	}
	if s.pty, err = ptyhost.New(ptyhost.Config{
		TranscriptDir: filepath.Join(cfg.DataDir, "transcripts"),
		Gate:          sshd.NewWriteGate(s.db),
		OnTitle: func(key ptyhost.SessionKey, title string) {
			forwardRunTitle(s.sched, key, title)
		},
		OnInput: func(key ptyhost.SessionKey, member domain.MemberID) {
			forwardRunSteer(s.sched, key, member)
		},
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
	s.control = control.New(control.Config{})
	if s.sched, err = scheduler.New(scheduler.Config{
		Store:         s.db,
		Runtime:       s.rt,
		Bus:           s.bus,
		Git:           lazyGit{s.git},
		PTY:           s.pty,
		StateDir:      filepath.Join(cfg.DataDir, "scheduler"),
		Homes:         homes,
		ReposDir:      filepath.Join(cfg.DataDir, "repos"),
		Profiles:      prof,
		StandardImage: cfg.StandardImage,
		BrowserImage:  cfg.BrowserImage,
		Control:       s.control,
		// What this build ships with, so the scheduler can tell a member
		// whether a server update would move their environment image.
		DefaultStandardImage: DefaultStandardImage,
		Harnesses:            cfg.Harnesses,
		StallThreshold:       cfg.StallThreshold,
		PollInterval:         cfg.PollInterval,
		CheckoutTTL:          cfg.CheckoutTTL,
		RunContainerTTL:      cfg.RunContainerTTL,
		MinFreeBytes:         cfg.MinFreeDiskBytes,
		ServerBinary:         cfg.ServerBinary,
	}); err != nil {
		return nil, err
	}
	authMu := &sync.Mutex{}
	if s.evidence, err = evidence.New(evidence.Config{
		Store:           s.db,
		Git:             s.git,
		Runs:            s.db,
		Transcript:      ptyTranscript{host: s.pty},
		Events:          s.log,
		Artifacts:       s.sched,
		EvidenceDir:     filepath.Join(cfg.DataDir, "evidence"),
		AuthorizationMu: authMu,
	}); err != nil {
		return nil, err
	}
	s.sched.UseEvidence(s.evidence)
	s.adapters = adapter.NewManager(s.bus, s.db, s.pty)
	whois := cfg.WhoIs
	var node reachability.Node
	var nodeErr error
	tailscaled := reachability.NewTailscale("")
	if _, statErr := os.Stat(sshd.DefaultTailscaledSocket); whois == nil && statErr == nil {
		whois = sshd.NewLocalWhoIs("")
		// Read the node once at startup; server.info reports the MagicDNS
		// name verbatim and the web gateway binds the addresses.
		// Best-effort for SSH: an unreachable LocalAPI leaves them empty.
		discoverCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		node, nodeErr = tailscaled.Self(discoverCtx)
		cancel()
	}
	workspaces := &workspaceDeletion{store: s.db, runs: s.sched, git: s.git, bus: s.bus}
	sshCfg := sshd.Config{
		Addr:              cfg.Addr,
		HostKeyPath:       HostKeyPath(cfg.DataDir),
		Store:             s.db,
		Bus:               s.bus,
		Git:               lazyGit{s.git},
		PTY:               s.pty,
		Runs:              s.sched,
		Control:           s.control,
		Homes:             homes,
		WhoIs:             whois,
		TailnetAutoJoin:   cfg.TailnetAutoJoin,
		TailnetRequireKey: cfg.TailnetRequireKey,
		TailnetHostname:   node.DNSName,
		InvitesDir:        filepath.Join(cfg.DataDir, "invites"),
		Profiles:          prof,
		Config:            sshd.NewConfigBackend(homes, s.db),
		AuthorizationMu:   authMu,
		DeleteWorkspace:   workspaces.Delete,
	}
	sshCfg.EdgeDeviceAutoApprove = cfg.EdgeDeviceAutoApprove
	if err = s.buildServices(Deps{
		Config:     cfg,
		DataDir:    cfg.DataDir,
		Store:      s.db,
		Bus:        s.bus,
		Events:     s.log,
		Runs:       s.sched,
		Runtime:    s.rt,
		Git:        s.git,
		PTY:        s.pty,
		SSH:        &sshCfg,
		Control:    s.control,
		Evidence:   s.evidence,
		Workspaces: workspaces,
	}); err != nil {
		return nil, err
	}
	if s.ssh, err = sshd.New(sshCfg); err != nil {
		return nil, err
	}
	if cfg.EdgeURL != "" {
		if s.edge, err = newEdgeAgent(cfg, s.ssh); err != nil {
			return nil, err
		}
		s.edgeWeb = servergw.EdgeConfig{
			SSH: s.ssh, Store: s.db, Agent: s.edge,
			EdgeURL:           cfg.EdgeURL,
			DeviceAutoApprove: cfg.EdgeDeviceAutoApprove,
			CertDir:           filepath.Join(edgeagent.StateDir(cfg.DataDir), "certs"),
			ACMEDirectory:     cfg.EdgeACMEDirectory,
		}
	}
	if cfg.WebPort != 0 {
		if cfg.WebPort < 0 || cfg.WebPort > 65535 {
			return nil, fmt.Errorf("server: web-port %d is not a port", cfg.WebPort)
		}
		if s.web, err = servergw.New(servergw.Config{SSH: s.ssh}); err != nil {
			return nil, fmt.Errorf("server: web-port %d: %w", cfg.WebPort, err)
		}
		if nodeErr != nil {
			return nil, fmt.Errorf("server: web-port %d: tailscaled did not report this node: %w", cfg.WebPort, nodeErr)
		}
		s.tailnet = servergw.Tailnet{Node: node, Port: cfg.WebPort, Certs: tailscaled}
	}
	return s, nil
}

// StorePath is the server's database under dataDir.
func StorePath(dataDir string) string { return filepath.Join(dataDir, "aether.db") }

// HostKeyPath is where the server keeps its SSH host key under dataDir.
// The key derives the server's id at an edge.
func HostKeyPath(dataDir string) string {
	return filepath.Join(dataDir, "ssh", "host_ed25519_key")
}

func newEdgeAgent(cfg Config, sshSrv *sshd.Server) (*edgeagent.Agent, error) {
	hostKey, err := sshd.LoadOrCreateHostKey(HostKeyPath(cfg.DataDir))
	if err != nil {
		return nil, err
	}
	return edgeagent.New(edgeagent.Config{EdgeURL: cfg.EdgeURL, DataDir: cfg.DataDir, HostKey: hostKey, SSH: sshSrv})
}

// serveEdgeDashboard serves the dashboard through the edge from the
// first time the edge holds the server claimed until ctx is done, and
// returns the gateway's close error. The hostname is <server id>.<server
// domain>, and only the edge knows the domain. Nothing here stops the
// server: an edge that passes no dashboard through, or a gateway that
// cannot be built, is logged and leaves SSH through the edge working.
func (s *Server) serveEdgeDashboard(ctx context.Context) error {
	domain, err := s.edge.ServerDomain(ctx)
	if err != nil {
		// Stopped before the first enrollment.
		return nil
	}
	if domain == "" {
		slog.Warn("server: the edge passes no dashboard through; the dashboard is not served through it", "edge", s.edgeWeb.EdgeURL)
		return nil
	}
	// The edge passes nothing through to an unclaimed server, so issuing
	// earlier only fails validations, which the CA rate-limits, and grows
	// the retry backoff that then holds the dashboard down after the claim.
	select {
	case <-s.edge.Claimed():
	case <-ctx.Done():
		return nil
	}
	cfg := s.edgeWeb
	cfg.ServerDomain = domain
	gw, err := servergw.NewEdge(cfg)
	if err != nil {
		slog.Error("server: the dashboard is not served through the edge", "error", err)
		return nil
	}
	// The gateway's Done is never watched: an edge outage or a
	// certificate failure leaves the server running.
	gw.Start()
	slog.Info("server: dashboard through the edge", "url", "https://"+gw.Host()+"/")
	<-ctx.Done()
	return gw.Close()
}

// WebURL is the address the dashboard is served at, empty when the
// server is SSH-only.
func (s *Server) WebURL() string {
	if s.web == nil {
		return ""
	}
	if s.tailnet.Port == 443 {
		return "https://" + s.tailnet.Node.DNSName + "/"
	}
	return fmt.Sprintf("https://%s:%d/", s.tailnet.Node.DNSName, s.tailnet.Port)
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
	if s.web != nil {
		if err := s.web.Start(ctx, s.tailnet); err != nil {
			return errors.Join(err, s.Close())
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 3)
	var wg sync.WaitGroup
	wg.Add(2)
	stopAgent := func() {}
	edgeWebDone := make(chan error, 1)
	if s.edge != nil {
		// The agent never ends the server: it retries an unreachable
		// edge until it is stopped. It stops after the edge gateway has
		// closed, because it closes the listener that gateway serves on.
		var agentCtx context.Context
		agentCtx, stopAgent = context.WithCancel(context.WithoutCancel(runCtx))
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.edge.Run(agentCtx)
		}()
		go func() { edgeWebDone <- s.serveEdgeDashboard(runCtx) }()
	} else {
		edgeWebDone <- nil
	}
	if s.web != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-s.web.Done():
				errc <- fmt.Errorf("server: web: %w", s.web.Err())
				cancel()
			case <-runCtx.Done():
			}
		}()
	}
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
	edgeWebErr := <-edgeWebDone
	stopAgent()
	wg.Wait()

	closeErr := errors.Join(edgeWebErr, s.Close())
	select {
	case err := <-errc:
		return errors.Join(err, closeErr)
	default:
	}
	return closeErr
}

// Close shuts the components down in dependency order: the PTY host is
// closed first so blocked/cancellable injections and attaches are released;
// transports then stop accepting requests, followed by scheduler/services,
// the git engine, and finally the bus, event log, runtime, and store.
// Idempotent.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		var errs []error
		closeAll := []func() error{}
		if s.pty != nil {
			closeAll = append(closeAll, s.pty.Close)
		}
		if s.web != nil {
			closeAll = append(closeAll, s.web.Close)
		}
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
