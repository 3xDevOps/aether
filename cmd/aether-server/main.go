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

	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/server"
	"github.com/3xDevOps/Aether/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		fmt.Println("aether-server", version.String())
	case "mcp":
		if err := mcp(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "aether-server:", err)
			os.Exit(1)
		}
	case "serve":
		if err := serve(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "aether-server:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "aether-server: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: aether-server <command>

commands:
  serve    run the server (flags: --data-dir, --addr, --neutral-image,
           --harness-definitions, --tailnet-auto-join, --tailnet-require-key,
           --conflict-coordination, --stall-threshold, --poll-interval,
           --checkout-ttl, --min-free-disk)
  version  print the version`)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dataDir := fs.String("data-dir", server.DefaultDataDir, "server data directory")
	addr := fs.String("addr", server.DefaultAddr, "SSH listen address")
	neutralImage := fs.String("neutral-image", server.DefaultNeutralImage,
		"server-owned neutral bootstrap image for workspaces without a custom image")
	harnessDefinitions := fs.String("harness-definitions", os.Getenv("AETHER_HARNESS_DEFINITIONS"),
		`JSON object of administrator-owned generic harness definitions`)
	tailnetAutoJoin := fs.Bool("tailnet-auto-join", false, "register unknown tailnet identities as approved members instead of pending")
	tailnetRequireKey := fs.Bool("tailnet-require-key", false, "additionally require pubkey verification on tailnet connections")
	conflictCoordination := fs.Bool("conflict-coordination", true, "let overlapping runs exchange coordination messages")
	stallThreshold := fs.Duration("stall-threshold", 0,
		"how long a run may go with no output and no file changes before it parks needs-attention (0 = 10m)")
	pollInterval := fs.Duration("poll-interval", 0, "how often stalls are checked (0 = 30s)")
	checkoutTTL := fs.Duration("checkout-ttl", 0,
		"how long a finished run's checkout is kept before it is garbage-collected (0 = 72h, negative = never)")
	minFreeDisk := fs.Int64("min-free-disk", 0,
		"refuse new runs below this many free bytes (0 = 1GiB, negative = no floor)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var harnesses map[string]scheduler.HarnessSpec
	if *harnessDefinitions != "" {
		if err := json.Unmarshal([]byte(*harnessDefinitions), &harnesses); err != nil {
			return fmt.Errorf("invalid --harness-definitions JSON: %w", err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv, err := server.New(ctx, server.Config{
		DataDir:           *dataDir,
		Addr:              *addr,
		NeutralImage:      *neutralImage,
		Harnesses:         harnesses,
		TailnetAutoJoin:   *tailnetAutoJoin,
		TailnetRequireKey: *tailnetRequireKey,

		CoordinationDisabled: !*conflictCoordination,

		StallThreshold:   *stallThreshold,
		PollInterval:     *pollInterval,
		CheckoutTTL:      *checkoutTTL,
		MinFreeDiskBytes: *minFreeDisk,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "aether-server %s serving SSH on %s (data dir %s)\n",
		version.String(), *addr, *dataDir)
	return srv.Run(ctx)
}
