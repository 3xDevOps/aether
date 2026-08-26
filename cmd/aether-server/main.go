// Command aether-server is the Aether server: embedded SSH transport,
// run scheduler, and event bus in a single binary.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/server"
	"github.com/3xDevOps/Aether/internal/serversetup"
	"github.com/3xDevOps/Aether/internal/version"
)

func main() {
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
	case "serve":
		exitOn(serve(args))
	case "install":
		exitOn(install(args))
	case "setup":
		exitOn(setup(args))
	case "config":
		exitOn(configCmd(args))
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
	os.Exit(1)
}

func usage() {
	_, _ = fmt.Fprintf(os.Stderr, `usage: aether-server <command>

commands:
  serve    run the server
  install  write the systemd unit and the config file (same options as serve)
  setup    walk through the install interactively
  config   show | path | set <key> <value> | edit
  version  print the version

serve and install options, which are also the config file keys:
  %s

run "aether-server serve -h" for what each one does, or keep them in %s
`, joinOptions(), serversetup.DefaultConfigPath)
}

// serveOptions holds the bound values of every `serve` option.
type serveOptions struct {
	dataDir              *string
	addr                 *string
	neutralImage         *string
	harnessDefinitions   *string
	tailnetAutoJoin      *bool
	tailnetRequireKey    *bool
	conflictCoordination *bool
	stallThreshold       *time.Duration
	pollInterval         *time.Duration
	checkoutTTL          *time.Duration
	minFreeDisk          *int64
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
	o.neutralImage = fs.String("neutral-image", server.DefaultNeutralImage,
		"server-owned neutral bootstrap image for workspaces without a custom image")
	o.harnessDefinitions = fs.String("harness-definitions", os.Getenv("AETHER_HARNESS_DEFINITIONS"),
		`JSON object of administrator-owned generic harness definitions`)
	o.tailnetAutoJoin = fs.Bool("tailnet-auto-join", false, "register unknown tailnet identities as approved members instead of pending")
	o.tailnetRequireKey = fs.Bool("tailnet-require-key", false, "additionally require pubkey verification on tailnet connections")
	o.conflictCoordination = fs.Bool("conflict-coordination", true, "let overlapping runs exchange coordination messages")
	o.stallThreshold = fs.Duration("stall-threshold", 0,
		"how long a run may go with no output and no file changes before it parks needs-attention (0 = 10m)")
	o.pollInterval = fs.Duration("poll-interval", 0, "how often stalls are checked (0 = 30s)")
	o.checkoutTTL = fs.Duration("checkout-ttl", 0,
		"how long a finished run's checkout is kept before it is garbage-collected (0 = 72h, negative = never)")
	o.minFreeDisk = fs.Int64("min-free-disk", 0,
		"refuse new runs below this many free bytes (0 = 1GiB, negative = no floor)")
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
	if *o.harnessDefinitions != "" {
		if err := json.Unmarshal([]byte(*o.harnessDefinitions), &harnesses); err != nil {
			return fmt.Errorf("invalid --harness-definitions JSON: %w", err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv, err := server.New(ctx, server.Config{
		DataDir:           *o.dataDir,
		Addr:              *o.addr,
		NeutralImage:      *o.neutralImage,
		Harnesses:         harnesses,
		TailnetAutoJoin:   *o.tailnetAutoJoin,
		TailnetRequireKey: *o.tailnetRequireKey,

		CoordinationDisabled: !*o.conflictCoordination,

		StallThreshold:   *o.stallThreshold,
		PollInterval:     *o.pollInterval,
		CheckoutTTL:      *o.checkoutTTL,
		MinFreeDiskBytes: *o.minFreeDisk,
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stderr, "aether-server %s serving SSH on %s (data dir %s)%s\n",
		version.String(), *o.addr, *o.dataDir, from)
	return srv.Run(ctx)
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
	if err := serversetup.Apply(fs, values); err != nil {
		return "", err
	}
	if len(values) == 0 {
		return "", nil
	}
	return " from " + path, nil
}
