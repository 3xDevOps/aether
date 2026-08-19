package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "dash",
		short: "forward the dashboard port and open a tokened browser tab",
		run:   runDash,
	})
}

func runDash(args []string) error {
	fs := flag.NewFlagSet("dash", flag.ExitOnError)
	urlOnly := fs.Bool("url", false, "print the dashboard URL instead of opening a browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := cli.Load()
	if err != nil {
		return err
	}
	conn, err := cli.Dial(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	// The control channel stays open for the session: it minted the token
	// and revokes it again on the way out.
	c, err := conn.Control()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	// Mint before anything else: the gateway also runs on a server that set
	// only --dashboard-addr, where there is no port to forward, and minting
	// is the one call that reports whether the gateway is up at all.
	// The dashboard has no login of its own, so this token is what carries
	// our SSH-proven identity onto HTTP.
	var token protocol.DashTokenMintResult
	if err = c.Call(protocol.MethodDashTokenMint, protocol.DashTokenMintParams{}, &token); err != nil {
		return err
	}
	var info protocol.ServerInfoResult
	if err = c.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil {
		return err
	}
	if token.URL != "" && (*urlOnly || info.DashboardPort == 0) {
		// The server is exposed directly: the URL needs no forward.
		fmt.Println(token.URL)
		if *urlOnly {
			// A printed URL is for scripting and outlives this process,
			// so the token cannot be revoked here; it lives until expiry.
			fmt.Fprintln(os.Stderr, "this token is not revoked on exit; it expires at "+token.ExpiresAt)
			return nil
		}
		defer revokeDashToken(c, token.Token)
		openBrowser(token.URL)
		waitForExit()
		return nil
	}
	if info.DashboardPort == 0 {
		return fmt.Errorf("this server forwards no dashboard port, and its direct address has no host to build a URL from: " +
			"start it with --dashboard-port, or bind --dashboard-addr to a concrete address")
	}
	defer revokeDashToken(c, token.Token)
	ln, err := conn.ListenLocalForward(0, info.DashboardPort)
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()
	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		return err
	}
	url := "http://127.0.0.1:" + portStr + "/?token=" + token.Token
	fmt.Println(url)
	if !*urlOnly {
		openBrowser(url)
	}
	waitForExit()
	return nil
}

func revokeDashToken(c *protocol.Client, token string) {
	_ = c.Call(protocol.MethodDashTokenRevoke, protocol.DashTokenRevokeParams{Token: token}, nil)
}

// waitForExit blocks until the process is told to stop. SIGTERM belongs
// here with Ctrl-C: without it a `kill` or a systemd stop skips the
// deferred revoke and leaves the token live. SIGHUP covers a closed
// terminal window the same way.
func waitForExit() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, append([]os.Signal{syscall.SIGHUP}, terminationSignals...)...)
	<-ch
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
