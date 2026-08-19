// Command aether-server is the Aether server: embedded SSH transport,
// run scheduler, event bus, and dashboard in a single binary.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

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
  serve    run the server (flags: --data-dir, --addr, --dashboard-port,
           --dashboard-addr, --tailnet-auto-join, --tailnet-require-key,
           --conflict-coordination, --stall-threshold, --poll-interval,
           --checkout-ttl, --min-free-disk)
  version  print the version`)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dataDir := fs.String("data-dir", server.DefaultDataDir, "server data directory")
	addr := fs.String("addr", server.DefaultAddr, "SSH listen address")
	dashboardPort := fs.Int("dashboard-port", 0, "local dashboard port reachable via SSH forwards (0 = deny)")
	tailnetAutoJoin := fs.Bool("tailnet-auto-join", false, "register unknown tailnet identities as approved members instead of pending")
	tailnetRequireKey := fs.Bool("tailnet-require-key", false, "additionally require pubkey verification on tailnet connections")
	dashboardAddr := fs.String("dashboard-addr", "", "expose the dashboard directly on this address (empty = loopback only)")
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := server.New(ctx, server.Config{
		DataDir:           *dataDir,
		Addr:              *addr,
		DashboardPort:     *dashboardPort,
		TailnetAutoJoin:   *tailnetAutoJoin,
		TailnetRequireKey: *tailnetRequireKey,
		DashboardAddr:     *dashboardAddr,

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
