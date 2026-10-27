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
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/adapter"
	"github.com/3xDevOps/Aether/internal/assistant"
	"github.com/3xDevOps/Aether/internal/assistant/tools"
	"github.com/3xDevOps/Aether/internal/collab"
	"github.com/3xDevOps/Aether/internal/connector"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	edgeagent "github.com/3xDevOps/Aether/internal/edge/agent"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/egress"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/fallback"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/integration"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/profile"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/quota"
	"github.com/3xDevOps/Aether/internal/reachability"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/secretstore"
	"github.com/3xDevOps/Aether/internal/servergw"
	"github.com/3xDevOps/Aether/internal/serverupdate"
	"github.com/3xDevOps/Aether/internal/signing"
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

const (
	tailscaledTimeout = 5 * time.Second
	webRetryMin       = 5 * time.Second
	// webRetryMax bounds how often a dashboard that cannot start asks
	// tailscaled for a certificate, which can mean a Let's Encrypt order.
	webRetryMax = 5 * time.Minute
)

// DefaultStandardImage is the published image matching this build.
var DefaultStandardImage = standardImageRepo + ":" + releaseImageTag(version.Version)

// releaseImageTag reduces a build version to a published image tag. The
// release workflow tags every published image with the release ref name, so
// a release version is its own image tag.
func releaseImageTag(buildVersion string) string {
	tag, _ := version.TrimDescribe(buildVersion)
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
	// enabled for the tailnet; without them the server runs without the
	// dashboard and server.info reports why.
	WebPort int
	// Runtime overrides the Docker runtime, primarily for tests.
	Runtime runtime.Runtime
	// Usage overrides the vendor quota reader, primarily for tests.
	Usage fallback.QuotaReader
	// WaitConditions replaces the watch service as the reader of the
	// conditions agents' waiting reports name, for tests.
	WaitConditions scheduler.WaitConditions
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
	// EdgeAccess is the server's edge access policy; empty is
	// PolicyApprovedDevices. The server enforces it and announces it to
	// the edge for display only.
	EdgeAccess edgeproto.AccessPolicy
	// EdgeReviewer lets a connector authorized by the edge's reviewer
	// account act as the member an admin bound it to.
	EdgeReviewer bool
	// CoordinationDisabled turns the conflict coordination kill switch off.
	// The zero value keeps coordination enabled, which is the shipped
	// default.
	CoordinationDisabled bool
	// HarnessUpdateDisabled stops pre-launch harness updates. The zero
	// value keeps them on, which is the shipped default.
	HarnessUpdateDisabled bool
	// WhoIs overrides tailnet identity resolution; nil keeps the default
	// (the local tailscaled socket when present). The E2E suite stubs it
	// so join and fallback scenarios need no real tailnet.
	WhoIs sshd.WhoIsResolver
	// SelfUpdate overrides the server self-update service's release feed
	// and restart mechanics; the zero value is the pinned GitHub releases
	// and no host controls. Store, Bus, StateDir, and Database are the
	// server's own and are ignored here. The E2E suite sets it so an update runs
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
	// often that is checked. FirstEventGrace and StreamQuietGrace shorten
	// StallThreshold for an Enhanced turn with no first update yet, or quiet
	// with no tool running. CheckoutTTL is how long a finished run's
	// checkout is kept before the GC reclaims it (negative disables GC).
	// RunContainerTTL retains exact containers after TUI Close, an agent-finished
	// TUI run, or mission-run completion (negative disables retention).
	// Completed mission workers never reopen.
	// MinFreeDiskBytes overrides the automatic data/runtime filesystem
	// reserve. Negative disables disk admission, not host memory checks.
	StallThreshold   time.Duration
	FirstEventGrace  time.Duration
	StreamQuietGrace time.Duration
	PollInterval     time.Duration
	CheckoutTTL      time.Duration
	RunContainerTTL  time.Duration
	MinFreeDiskBytes int64
	// Run budgets apply to runs and member environments. Zero selects
	// automatic up-to-8 CPUs, 8 GiB memory, and 4096 processes.
	RunCPULimit    float64
	RunMemoryBytes int64
	RunPidsLimit   int64

	// ContainerRuntime is the OCI runtime of run and member environment
	// containers: DefaultContainerRuntime picks sysbox-runc when Docker has
	// it registered, empty means the Docker daemon default.
	ContainerRuntime string

	// SysboxInstaller lets admins install Sysbox on the host through the
	// control channel. Only cmd/aether-server sets it; an embedded server
	// never runs host installers.
	SysboxInstaller bool
}

const DefaultContainerRuntime = runtime.RuntimeAuto

// Server is the assembled Aether server.
type Server struct {
	db        *store.DB
	log       *events.SQLiteLog
	bus       *events.InProc
	rt        runtime.Runtime
	docker    *runtime.Docker // set only when the server constructed it
	egress    *egress.Proxy   // set with docker
	git       *gitengine.Engine
	pty       *ptyhost.Host
	sched     *scheduler.Scheduler
	signing   *signing.Service
	assistant *assistant.Service
	control   *control.Service
	evidence  *evidence.Service
	adapters  *adapter.Manager
	ssh       *sshd.Server
	edge      *edgeagent.Agent
	web       *servergw.Gateway
	tailnet   servergw.Tailnet
	services  []namedService

	// controllerMu orders run.controller publishes: each reads the holder
	// under it, so the last one published names the current holder.
	controllerMu sync.Mutex

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

// ptyTranscript exports the retained window and its atomic prefix-expiry
// metadata. Evidence independently bounds and preserves its artifact copy.
type ptyTranscript struct{ host *ptyhost.Host }

func (p ptyTranscript) Replay(run domain.RunID) (io.ReadCloser, bool, error) {
	window, err := p.host.Replay(run)
	return window.Reader, window.TruncatedBefore, err
}

// New constructs every component from cfg, fanning the data directory out
// per the Wave 1 contract's layout. The PTY write gate enforces the Wave 3
// permission model (steer capability) against the store.
func New(ctx context.Context, cfg Config) (srv *Server, err error) {
	if err = scheduler.ValidateRunBudgets(cfg.RunCPULimit, cfg.RunMemoryBytes, cfg.RunPidsLimit); err != nil {
		return nil, err
	}
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
		if s.docker, err = runtime.NewDocker(runtime.WithContainerRuntime(cfg.ContainerRuntime)); err != nil {
			return nil, err
		}
		s.rt = s.docker
	}
	removeAgentFiles := runtime.Remover(s.rt, cfg.StandardImage)

	if s.git, err = gitengine.New(gitengine.Config{
		ReposDir:       filepath.Join(cfg.DataDir, "repos"),
		CheckoutsDir:   filepath.Join(cfg.DataDir, "checkouts"),
		Bus:            s.bus,
		EventLog:       s.log,
		RemoveCheckout: removeAgentFiles,
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
	homes, herr := memberhome.New(homesRoot, filepath.Join(cfg.DataDir, "home-caches"), removeAgentFiles)
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
	if s.signing, err = signing.New(filepath.Join(cfg.DataDir, "signing-keys"), filepath.Join(cfg.DataDir, "sign"), homes); err != nil {
		return nil, err
	}
	if rerr := os.RemoveAll(filepath.Join(cfg.DataDir, "toolenv")); rerr != nil {
		return nil, fmt.Errorf("server: remove legacy toolenv: %w", rerr)
	}
	s.control = control.New(control.Config{OnHolderChange: s.publishController})
	secrets := secretstore.New(filepath.Join(cfg.DataDir, "workspace-secrets"))
	mem, err := newMemory(Deps{Store: s.db, Bus: s.bus, Events: s.log})
	if err != nil {
		return nil, fmt.Errorf("server: build team memory: %w", err)
	}
	var egressProxy scheduler.EgressProxy
	if s.docker != nil {
		s.egress = egress.New(s.docker, s.db, s.bus)
		egressProxy = s.egress
	}
	if s.sched, err = scheduler.New(scheduler.Config{
		Store:          s.db,
		Runtime:        s.rt,
		Bus:            s.bus,
		WaitConditions: cfg.WaitConditions,
		Egress:         egressProxy,
		EventSeq:       s.log.LastSeq,
		Git:            lazyGit{s.git},
		PTY:            s.pty,
		StateDir:       filepath.Join(cfg.DataDir, "scheduler"),
		DataDir:        cfg.DataDir,
		Homes:          homes,
		Signing:        s.signing,
		Memory:         mem,
		RemoveAll:      removeAgentFiles,
		Secrets:        secrets,
		ReposDir:       filepath.Join(cfg.DataDir, "repos"),
		Profiles:       prof,
		StandardImage:  cfg.StandardImage,
		BrowserImage:   cfg.BrowserImage,
		Control:        s.control,
		// What this build ships with, so the scheduler can tell a member
		// whether a server update would move their environment image.
		DefaultStandardImage: DefaultStandardImage,
		Harnesses:            cfg.Harnesses,
		StallThreshold:       cfg.StallThreshold,
		FirstEventGrace:      cfg.FirstEventGrace,
		StreamQuietGrace:     cfg.StreamQuietGrace,
		PollInterval:         cfg.PollInterval,
		CheckoutTTL:          cfg.CheckoutTTL,
		RunContainerTTL:      cfg.RunContainerTTL,
		MinFreeBytes:         cfg.MinFreeDiskBytes,
		RunCPULimit:          cfg.RunCPULimit,
		RunMemoryBytes:       cfg.RunMemoryBytes,
		RunPidsLimit:         cfg.RunPidsLimit,
		ServerBinary:         cfg.ServerBinary,
		AssistantDir:         filepath.Join(cfg.DataDir, "assistant"),

		HarnessUpdateDisabled: cfg.HarnessUpdateDisabled,
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
	s.adapters = adapter.NewManager(s.bus, s.db, s.pty, s.sched)
	whois := cfg.WhoIs
	var node reachability.Node
	tailscaled := reachability.NewTailscale("")
	if _, statErr := os.Stat(sshd.DefaultTailscaledSocket); whois == nil && statErr == nil {
		whois = sshd.NewLocalWhoIs("")
		// Read the node once at startup; server.info reports the MagicDNS
		// name verbatim. Best-effort: an unreachable LocalAPI leaves it
		// empty.
		discoverCtx, cancel := context.WithTimeout(ctx, tailscaledTimeout)
		node, _ = tailscaled.Self(discoverCtx)
		cancel()
	}
	workspaces := &workspaceDeletion{store: s.db, runs: s.sched, git: s.git, bus: s.bus, secrets: secrets}
	sshCfg := sshd.Config{
		Addr:              cfg.Addr,
		HostKeyPath:       HostKeyPath(cfg.DataDir),
		Store:             s.db,
		RunLimits:         scheduler.ResolveRunLimits(domain.RunLimits{CPUs: cfg.RunCPULimit, MemoryBytes: cfg.RunMemoryBytes, Pids: cfg.RunPidsLimit}),
		Bus:               s.bus,
		Git:               lazyGit{s.git},
		PTY:               s.pty,
		Runs:              s.sched,
		Control:           s.control,
		Homes:             homes,
		Signing:           s.signing,
		Secrets:           secrets,
		WhoIs:             whois,
		TailnetAutoJoin:   cfg.TailnetAutoJoin,
		TailnetRequireKey: cfg.TailnetRequireKey,
		TailnetHostname:   node.DNSName,
		InvitesDir:        filepath.Join(cfg.DataDir, "invites"),
		MergedDir:         filepath.Join(cfg.DataDir, "merged"),
		Profiles:          prof,
		Config:            sshd.NewConfigBackend(homes, s.db),
		AuthorizationMu:   authMu,
		DeleteWorkspace:   workspaces.Delete,
	}
	s.assistant = assistant.New(assistant.Config{
		Dir:    filepath.Join(cfg.DataDir, "assistant"),
		Store:  s.db,
		Homes:  homes,
		Runs:   s.sched,
		Caller: func(member domain.MemberID) tools.Caller { return s.ssh.AssistantLocal(member) },
		Bus:    s.bus,
	})
	sshCfg.Services.Assistant = s.assistant
	sshCfg.EdgeAccess = cfg.EdgeAccess
	sshCfg.EdgeReviewer = cfg.EdgeReviewer
	sshCfg.Services.Usage = cfg.Usage
	if sshCfg.Services.Usage == nil {
		sshCfg.Services.Usage = quota.New(homes)
	}
	// The name the edge agent announces; an unreadable one leaves refusals
	// unnamed.
	sshCfg.Name, _ = os.Hostname()
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
		SSHServer:  func() *sshd.Server { return s.ssh },
		Control:    s.control,
		Evidence:   s.evidence,
		Workspaces: workspaces,
		Assistant:  s.assistant,
		Memory:     mem,
	}); err != nil {
		return nil, err
	}
	rooms := sshCfg.Services.Rooms
	s.sched.UseUsageFallback(scheduler.UsageFallback{
		Usage:           sshCfg.Services.Usage,
		AuthorizationMu: authMu,
		Note: func(ctx context.Context, run *domain.Run, body, key string) error {
			if rooms == nil {
				return errors.New("the room service is not configured")
			}
			_, postErr := rooms.Post(ctx, collab.MessageInput{
				WorkspaceID: run.WorkspaceID, RunID: run.ID, ActorID: run.MemberID,
				Kind: store.RoomMessageSystem, Body: body, IdempotencyKey: key,
			})
			return postErr
		},
	})
	if s.ssh, err = sshd.New(sshCfg); err != nil {
		return nil, err
	}
	if err = s.ssh.RecoverMergeArchives(ctx); err != nil {
		return nil, err
	}
	if err = s.signing.Migrate(ctx, members); err != nil {
		return nil, fmt.Errorf("server: move signing keys out of member homes: %w", err)
	}
	// Containers that outlive a server restart still mount their member's
	// agent.
	for _, member := range members {
		if _, err = s.signing.Serve(member.ID); err != nil {
			return nil, err
		}
	}
	if cfg.EdgeURL != "" {
		if s.edge, err = newEdgeAgent(cfg, s.db, s.ssh); err != nil {
			return nil, err
		}
		s.ssh.SetEdgeOwner(s.edge)
	}
	if cfg.WebPort != 0 {
		if cfg.WebPort < 0 || cfg.WebPort > 65535 {
			return nil, fmt.Errorf("server: web-port %d is not a port", cfg.WebPort)
		}
		s.tailnet = servergw.Tailnet{Port: cfg.WebPort, Certs: tailscaled}
		var webErr error
		if s.web, webErr = servergw.New(servergw.Config{SSH: s.ssh}); webErr != nil {
			s.webFailed(fmt.Errorf("server: web-port %d: %w", cfg.WebPort, webErr))
		}
	}
	return s, nil
}

// webFailed reports why the tailnet dashboard is not serving. The
// dashboard is optional, so SSH and the edge keep serving.
func (s *Server) webFailed(err error) {
	slog.Error("server: the tailnet dashboard is not serving; the server runs without it", "error", err)
	s.ssh.SetTailnetDashboard("", err)
}

// serveWeb starts the tailnet dashboard and serves it until ctx ends. A
// failed start is retried: at boot tailscaled can answer before it knows
// the node, and HTTPS certificates can be enabled while the server runs.
// Each new reason is reported once, and cleared once the dashboard serves.
func (s *Server) serveWeb(ctx context.Context) {
	tn := s.tailnet
	reported := ""
	for delay := webRetryMin; ; delay = min(2*delay, webRetryMax) {
		lookupCtx, cancel := context.WithTimeout(ctx, tailscaledTimeout)
		node, err := tn.Certs.Self(lookupCtx)
		cancel()
		if err != nil {
			err = fmt.Errorf("server: web-port %d: tailscaled did not report this node: %w", tn.Port, err)
		} else {
			tn.Node = node
			err = s.web.Start(ctx, tn)
		}
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return
		}
		if err.Error() != reported {
			reported = err.Error()
			s.webFailed(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
	url := fmt.Sprintf("https://%s:%d", tn.Node.DNSName, tn.Port)
	if tn.Port == 443 {
		url = "https://" + tn.Node.DNSName
	}
	s.ssh.SetTailnetDashboard(url, nil)
	slog.Info("server: serving the dashboard", "url", url+"/")
	select {
	case <-s.web.Done():
		s.webFailed(fmt.Errorf("server: web: %w", s.web.Err()))
	case <-ctx.Done():
	}
}

// StorePath is the server's database under dataDir.
func StorePath(dataDir string) string { return filepath.Join(dataDir, "aether.db") }

// HostKeyPath is where the server keeps its SSH host key under dataDir.
// The key derives the server's id at an edge.
func HostKeyPath(dataDir string) string {
	return filepath.Join(dataDir, "ssh", "host_ed25519_key")
}

func newEdgeAgent(cfg Config, db *store.DB, sshSrv *sshd.Server) (*edgeagent.Agent, error) {
	hostKey, err := sshd.LoadOrCreateHostKey(HostKeyPath(cfg.DataDir))
	if err != nil {
		return nil, err
	}
	origin, err := edgeproto.Origin(cfg.EdgeURL)
	if err != nil {
		return nil, fmt.Errorf("server: edge-url: %w", err)
	}
	mcp, err := connector.New(connector.Config{
		BaseURL: origin + edgeproto.MCPPath(edgeproto.ServerID(hostKey.PublicKey())),
		Store:   db,
		SSH:     sshSrv,
		KeyPath: filepath.Join(cfg.DataDir, "connector", "hmac.key"),
	})
	if err != nil {
		return nil, err
	}
	return edgeagent.New(edgeagent.Config{EdgeURL: cfg.EdgeURL, DataDir: cfg.DataDir, HostKey: hostKey, SSH: sshSrv,
		HTTP: mcp, AccessPolicy: cfg.EdgeAccess})
}

// Store is the server's persistence layer.
func (s *Server) Store() store.Store { return s.db }

// Bus is the server's event bus.
func (s *Server) Bus() events.Bus { return s.bus }

// SSHAddr returns the bound SSH listen address, or nil before Run has
// bound it.
func (s *Server) SSHAddr() net.Addr { return s.ssh.Addr() }

// Recovered is closed once the scheduler has reconciled the runs and
// terminals from before this start.
func (s *Server) Recovered() <-chan struct{} { return s.sched.RecoveryReady() }

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
	if s.egress != nil {
		// Not fatal: only egress-limited runs need the proxy, and the next
		// launch of one tries again.
		if err := s.egress.Resume(ctx); err != nil {
			slog.Warn("server: resume the egress proxy", "error", err)
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	if s.edge != nil {
		// The agent never ends the server: it retries an unreachable
		// edge until runCtx is done.
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.edge.Run(runCtx)
		}()
	}
	if s.web != nil {
		// Nor does the dashboard, and SSH does not wait on its
		// certificate fetch.
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.serveWeb(runCtx)
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
	wg.Wait()

	closeErr := s.Close()
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
		if s.assistant != nil {
			closeAll = append(closeAll, s.assistant.Close)
		}
		if s.sched != nil {
			closeAll = append(closeAll, s.sched.Close)
		}
		if s.signing != nil {
			closeAll = append(closeAll, s.signing.Close)
		}
		if s.adapters != nil {
			closeAll = append(closeAll, s.adapters.Close)
		}
		// Registered services publish events, so they stop before the bus.
		closeAll = append(closeAll, s.closeServices()...)
		if s.egress != nil {
			closeAll = append(closeAll, s.egress.Close)
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
