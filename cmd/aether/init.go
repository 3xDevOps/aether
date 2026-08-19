package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/reachability"
)

func init() {
	register(command{
		name:  "init",
		short: "prepare a server data directory (does not start the server)",
		run:   runInit,
	})
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	dataDir := fs.String("data-dir", "/var/lib/aether", "server data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	abs, err := filepath.Abs(*dataDir)
	if err != nil {
		abs = *dataDir
	}
	fmt.Printf("data directory: %s\n", abs)

	if _, err := os.Stat(reachability.DefaultTailscaledSocket); err == nil {
		fmt.Printf("tailscale: socket %s detected\n", reachability.DefaultTailscaledSocket)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ep, derr := reachability.NewTailscale("").Discover(ctx)
		cancel()
		if derr == nil {
			fmt.Printf("tailscale: tailnet hostname %s\n", ep.Host)
		} else if line := tailscaleStatusLine(); line != "" {
			// LocalAPI socket exists but is not readable by this user
			// (client-side case): fall back to the tailscale CLI.
			fmt.Printf("tailscale: %s\n", line)
		} else {
			fmt.Println("tailscale: status unavailable (is tailscaled running?)")
		}
	} else {
		fmt.Println("tailscale: not detected (key auth / invite join still work)")
	}

	fmt.Println()
	fmt.Println("start the server:")
	fmt.Printf("  aether-server serve --data-dir %s --addr :2222\n", abs)
	fmt.Println()
	fmt.Println("then from a client:")
	fmt.Println("  aether link <host>:2222")
	fmt.Println("  aether link <host>:2222 --invite <code>   # non-tailnet join")
	return nil
}

// tailscaleStatusLine shells out to `tailscale status` and returns its
// first line, or "" when the CLI is unavailable or errors.
func tailscaleStatusLine() string {
	out, err := exec.Command("tailscale", "status").Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(out))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return line
}
