// Command aether-server is the Aether server: embedded SSH transport,
// run scheduler, and event bus in a single binary.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/3xDevOps/Aether/internal/coordcli"
	edgeagent "github.com/3xDevOps/Aether/internal/edge/agent"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/server"
	"github.com/3xDevOps/Aether/internal/serversetup"
	"github.com/3xDevOps/Aether/internal/serverupdate"
	"github.com/3xDevOps/Aether/internal/version"
)

func main() {
	// The lifecycle helper must precede the aether-internal basename dispatch:
	// every run receives that read-only, version-matched staged binary.
	if len(os.Args) > 1 && os.Args[1] == "dev-exec" {
		os.Exit(devExec(os.Args[2:]))
	}
	if filepath.Base(os.Args[0]) == "aether-internal" {
		os.Exit(coordcli.Main(os.Args[1:]))
	}
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "version":
		fmt.Println("aether-server", version.String())
	case "mcp":
		exitOn(mcp(args))
	case "report":
		report(args)
	case "package-exchange":
		if len(args) != 2 {
			exitOn(fmt.Errorf("usage: aether-server package-exchange <installed> <staged>"))
		}
		exitOn(harness.ExchangePackages(args[0], args[1]))
	case "serve":
		exitOn(serve(args))
	case "install":
		exitOn(install(args))
	case "setup":
		exitOn(setup(args))
	case "sysbox":
		exitOn(sysboxCmd(args))
	case "config":
		exitOn(configCmd(args))
	case "edge":
		exitOn(edgeCmd(args))
	case "device":
		exitOn(deviceCmd(args))
	default:
		_, _ = fmt.Fprintf(os.Stderr, "aether-server: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func exitOn(err error) {
	if err == nil {
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "aether-server:", err)
	if errors.Is(err, serverupdate.ErrRestartUnit) {
		os.Exit(75)
	}
	os.Exit(1)
}

func usage() {
	_, _ = fmt.Fprintf(os.Stderr, `usage: aether-server <command>

commands:
  serve    run the server
  install  write the systemd unit and the config file (same options as serve)
  setup    walk through the install interactively
  sysbox   status | install: let runs start their own Docker daemon
  config   show | path | set <key> <value> | edit
  edge     status | claim-code | trust | leave
  device   approve <code> | review: approve or revoke members' edge devices
  version  print the version

serve and install options, which are also the config file keys:
  %s

run "aether-server serve -h" for what each one does, or keep them in %s
`, joinOptions(), serversetup.DefaultConfigPath)
}

type serveOptions struct {
	dataDir              *string
	addr                 *string
	webPort              *int
	standardImage        *string
	browserImage         *string
	agentDefinitions     *string
	tailnetAutoJoin      *bool
	tailnetRequireKey    *bool
	edgeURL              *edgeURLValue
	edgeAccess           *accessPolicyValue
	conflictCoordination *bool
	agentUpdate          *bool
	stallThreshold       *time.Duration
	pollInterval         *time.Duration
	checkoutTTL          *time.Duration
	runContainerTTL      *time.Duration
	minFreeDisk          *int64
	runCPUs              *runCPUValue
	runMemory            *runCountValue
	runPids              *runCountValue
	containerRuntime     *string
}

// serveFlags declares the server options on fs. It is the single definition
// of the option list: `serve` binds it, `install` reads the flags it was
// given off it, and `config` validates config-file keys against it, so a
// config key can never drift from a flag name. Deliberately absent is
// --config itself, which selects the file rather than living in it.
func serveFlags(fs *flag.FlagSet) *serveOptions {
	o := &serveOptions{}
	o.dataDir = fs.String("data-dir", server.DefaultDataDir, "server data directory")
	o.addr = fs.String("addr", server.DefaultAddr, "SSH listen address")
	o.webPort = fs.Int("web-port", 0,
		"serve the dashboard over HTTPS on this host's tailnet addresses at this port (0 = off; 443 makes it https://<magicdns-name>/)")
	o.standardImage = fs.String("standard-image", server.DefaultStandardImage,
		"published standard environment image recommended at workspace creation")
	browserImage := os.Getenv("AETHER_BROWSER_IMAGE")
	if browserImage == "" {
		browserImage = server.DefaultBrowserImage
	}
	o.browserImage = fs.String("browser-image", browserImage,
		"versioned browser companion image or digest (AETHER_BROWSER_IMAGE; never untagged or latest)")
	o.agentDefinitions = fs.String("agent-definitions", os.Getenv("AETHER_AGENT_DEFINITIONS"),
		`JSON object of administrator-owned agent definitions (AETHER_AGENT_DEFINITIONS)`)
	o.tailnetAutoJoin = fs.Bool("tailnet-auto-join", false, "register unknown tailnet identities as approved members instead of pending")
	o.tailnetRequireKey = fs.Bool("tailnet-require-key", false, "additionally require pubkey verification on tailnet connections")
	o.edgeURL = new(edgeURLValue)
	fs.Var(o.edgeURL, "edge-url",
		"edge that relays SSH for members without a direct or tailnet route (empty = off; aether-server setup offers "+edgeagent.DefaultURL+")")
	o.edgeAccess = new(accessPolicyValue(edgeproto.PolicyApprovedDevices))
	fs.Var(o.edgeAccess, "edge-access",
		"who may reach this server through the edge: account (signing in with GitHub is enough) or approved-devices "+
			"(each new device waits until a person approves it); changed only on this host")
	o.conflictCoordination = fs.Bool("conflict-coordination", true, "let overlapping runs exchange coordination messages")
	o.agentUpdate = fs.Bool("agent-update", true, "update a shipped agent installed in the member home before launching it")
	o.stallThreshold = fs.Duration("stall-threshold", 0,
		"how long a run may go with no output and no file changes before it parks needs-attention (0 = 10m)")
	o.pollInterval = fs.Duration("poll-interval", 0, "how often stalls are checked (0 = 30s)")
	o.checkoutTTL = fs.Duration("checkout-ttl", 0,
		"how long a finished run's checkout is kept before it is garbage-collected (0 = 72h, negative = never)")
	o.runContainerTTL = fs.Duration("run-container-ttl", 0,
		"how long explicitly closed Standard and Enhanced run containers and completed swarm worker containers are retained (0 = 168h, negative = no retention)")
	o.minFreeDisk = fs.Int64("min-free-disk", 0,
		"free-byte reserve on data/runtime filesystems (0 = automatic 5-20GiB, negative = disable disk admission)")
	o.runCPUs = new(runCPUValue)
	fs.Var(o.runCPUs, "run-cpus", "CPU ceiling per run/member environment (0 = automatic, up to 8 host CPUs)")
	o.runMemory = new(runCountValue)
	fs.Var(o.runMemory, "run-memory", "memory ceiling in bytes per run/member environment (0 = automatic 8GiB)")
	o.runPids = new(runCountValue)
	fs.Var(o.runPids, "run-pids", "process ceiling per run/member environment (0 = automatic 4096)")
	o.containerRuntime = fs.String("container-runtime", server.DefaultContainerRuntime,
		"OCI runtime for run and member environment containers: auto uses sysbox-runc when Docker has it registered "+
			"(Docker inside runs without --privileged), otherwise the daemon default; any other value is used as given, e.g. runc to opt out")
	return o
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", serversetup.DefaultConfigPath,
		"options file; values here lose to flags passed on the command line")
	o := serveFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	from, configErr := applyConfigFile(fs, *configPath, *configPath == serversetup.DefaultConfigPath)
	if configErr != nil {
		return configErr
	}

	var harnesses map[string]scheduler.HarnessSpec
	if *o.agentDefinitions != "" {
		if err := json.Unmarshal([]byte(*o.agentDefinitions), &harnesses); err != nil {
			return fmt.Errorf("invalid --agent-definitions JSON: %w", err)
		}
	}
	// The last update's swap record is read before the store opens: a
	// start that has to roll back must do so before this binary migrates
	// the database.
	host := serverupdate.HostProcess()
	stateDir := server.UpdateStateDir(*o.dataDir)
	trial, err := serverupdate.Boot(stateDir, host)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, stopServe := context.WithCancelCause(ctx)
	defer stopServe(nil)
	var (
		drainMu sync.Mutex
		drained []string
	)
	drain := func(argv []string) {
		drainMu.Lock()
		drained = argv
		drainMu.Unlock()
		stopServe(errors.New("restarting for a server update"))
	}
	srv, err := server.New(ctx, server.Config{
		DataDir:           *o.dataDir,
		Addr:              *o.addr,
		WebPort:           *o.webPort,
		StandardImage:     *o.standardImage,
		BrowserImage:      *o.browserImage,
		Harnesses:         harnesses,
		TailnetAutoJoin:   *o.tailnetAutoJoin,
		TailnetRequireKey: *o.tailnetRequireKey,

		EdgeURL:    string(*o.edgeURL),
		EdgeAccess: edgeproto.AccessPolicy(*o.edgeAccess),

		CoordinationDisabled: !*o.conflictCoordination,

		HarnessUpdateDisabled: !*o.agentUpdate,

		// The one place a process is granted the right to replace itself
		// and to ask systemd for a restart. Nothing else supplies it, so
		// no test binary can reach the host's service manager.
		SelfUpdate: serverupdate.Config{Host: host, Drain: drain},

		StallThreshold:   *o.stallThreshold,
		PollInterval:     *o.pollInterval,
		CheckoutTTL:      *o.checkoutTTL,
		RunContainerTTL:  *o.runContainerTTL,
		MinFreeDiskBytes: *o.minFreeDisk,
		RunCPULimit:      float64(*o.runCPUs),
		RunMemoryBytes:   int64(*o.runMemory),
		RunPidsLimit:     int64(*o.runPids),

		ContainerRuntime: *o.containerRuntime,
		SysboxInstaller:  true,
	})
	if err != nil {
		return trial.Fail(err)
	}
	// server.New has migrated the store, so the trial's deadline starts
	// now. At the deadline the process exits rather than cancelling ctx: a
	// start stuck somewhere that ignores ctx would otherwise never end.
	deadline, err := trial.Migrated()
	if err != nil {
		return trial.Fail(errors.Join(err, srv.Close()))
	}
	if !deadline.IsZero() {
		timer := time.AfterFunc(time.Until(deadline), func() {
			if expired := trial.Expire(); expired != nil {
				slog.Error("serverupdate: exiting so the next start rolls back", "error", expired)
				os.Exit(1)
			}
		})
		defer timer.Stop()
	}
	go commitWhenServing(ctx, srv, trial)
	web := ""
	if url := srv.WebURL(); url != "" {
		web = " and the dashboard on " + url
	}
	_, _ = fmt.Fprintf(os.Stderr, "aether-server %s serving SSH on %s%s (data dir %s)%s\n",
		version.String(), *o.addr, web, *o.dataDir, from)
	warnIfUnitOutdated()
	err = srv.Run(ctx)
	drainMu.Lock()
	argv := drained
	drainMu.Unlock()
	if argv != nil {
		return errors.Join(err, serverupdate.ExecAfterDrain(stateDir, host, argv))
	}
	if err != nil {
		return trial.Fail(err)
	}
	return nil
}

// commitWhenServing commits the start once the SSH listener is bound and
// the scheduler has recovered the runs from before it: the store opened
// and migrated in server.New, so the new binary is known to work.
func commitWhenServing(ctx context.Context, srv *server.Server, trial *serverupdate.Trial) {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for srv.SSHAddr() == nil {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
	select {
	case <-ctx.Done():
		return
	case <-srv.Recovered():
	}
	if err := trial.Commit(ctx, srv.Store(), srv.Bus()); err != nil {
		slog.Error("serverupdate: record the update outcome", "error", err)
	}
}

// applyConfigFile loads path and applies it to fs, which must already be
// parsed: values only reach flags the operator did not pass, so an explicit
// flag always wins. optional tolerates a missing file, which is the case for
// the default path - the shipped unit names it, and an install that never
// wrote a config runs on the flag defaults. Anywhere else a missing file is
// a typo worth stopping for. It returns the suffix the startup line uses to
// name the file that is live.
func applyConfigFile(fs *flag.FlagSet, path string, optional bool) (string, error) {
	if !optional {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("--config: %w", err)
		}
	}
	values, err := serversetup.Load(path)
	if err != nil {
		return "", err
	}
	retired, err := serversetup.Apply(fs, values)
	if err != nil {
		return "", err
	}
	// A key left over from a removed feature is worth saying out loud once
	// - the operator set it deliberately and it is no longer doing
	// anything - but it is not worth refusing to boot over.
	for _, key := range retired {
		slog.Warn("config: option no longer exists and is ignored", "option", key, "file", path)
	}
	if len(values) == 0 {
		return "", nil
	}
	return " from " + path, nil
}

// warnIfUnitOutdated tells the operator when the installed systemd unit is not
// the one this release ships. It only reads the unit: a daemon-reload has
// host-wide effects, so applying the new unit stays the operator's call.
func warnIfUnitOutdated() {
	if os.Getenv("INVOCATION_ID") == "" {
		return
	}
	outdated, err := serversetup.UnitOutdated(serversetup.UnitPath)
	if err != nil {
		slog.Warn("could not check whether the systemd unit is up to date", "error", err)
		return
	}
	if outdated {
		slog.Warn("the installed systemd unit differs from this release's; apply it with: "+
			"sudo aether-server install --sysbox=false && sudo systemctl daemon-reload && sudo systemctl restart "+serversetup.ServiceName,
			"unit", serversetup.UnitPath)
	}
}
