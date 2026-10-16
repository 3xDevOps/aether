package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/shellquote"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/version"
)

func init() {
	register(command{
		name:  "update",
		short: "update this binary (and aether-server beside it) to a release",
		run:   runUpdate,
	})
}

func runUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	tag := fs.String("version", "", "release tag to install (default: latest)")
	check := fs.Bool("check", false, "only report whether an update is available")
	jsonOut := fs.Bool("json", false, "with --check, print the check as one JSON line")
	noApp := fs.Bool("no-app", false, "skip rebuilding the installed desktop app")
	force := fs.Bool("force", false, "install even when the linked server has not been updated to the release first")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *jsonOut && !*check {
		return errors.New("--json only reports a check; re-run as: aether update --check --json")
	}
	if *check && *tag != "" {
		return errors.New("--version installs a tag and --check installs nothing; pick one")
	}
	if *check && *noApp {
		return errors.New("--no-app skips part of an install and --check installs nothing; pick one")
	}
	if *check && *force {
		return errors.New("--force skips a check before an install and --check installs nothing; pick one")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if *check {
		return reportCheck(ctx, *jsonOut)
	}
	if app := os.Getenv(selfupdate.PackagedEnv); app != "" {
		return fmt.Errorf("this aether is part of the %s, which installs its updates; update the %s instead", app, app)
	}

	if runtime.GOOS == "windows" {
		// Refuse before the network: selfupdate.Update refuses too, but
		// there is no reason to resolve a tag that cannot be installed.
		return selfupdate.ErrWindows
	}
	base := "https://github.com/" + selfupdate.Repo
	var server linkedServer
	if !*force {
		server = probeLinkedServer(ctx)
	}
	if match := matchServer(server); *tag == "" && match != "" {
		*tag = match
		fmt.Printf("installing %s to match the server\n", *tag)
	} else if *tag == "" {
		got, err := selfupdate.DefaultChecker().CheckFresh(ctx)
		if err != nil {
			return err
		}
		if !got.UpdateAvailable {
			printCheck(got)
			return reportBinary()
		}
		*tag = got.Latest
	} else if version.Version == *tag {
		fmt.Printf("already on %s\n", *tag)
		return reportBinary()
	}

	if err := serverFirst(os.Stdout, *tag, server); err != nil {
		return err
	}

	self, err := os.Executable()
	if err == nil {
		self, err = filepath.EvalSymlinks(self)
	}
	if err != nil {
		return fmt.Errorf("locate this binary: %w", err)
	}
	if err = checkServerSchema(ctx, base, *tag, self); err != nil {
		return err
	}

	fmt.Printf("updating aether %s -> %s\n", version.String(), *tag)
	replaced, err := selfupdate.Update(ctx, base, *tag)
	for _, path := range replaced {
		fmt.Printf("updated %s\n", path)
	}
	if err != nil {
		return err
	}
	if len(replaced) > 1 {
		fmt.Printf("aether-server on disk is now %s; the running server keeps its old version until you restart it: sudo systemctl restart aether-server\n", *tag)
		if offerSysbox(ctx, "/etc/os-release", dockerRuntimes) {
			fmt.Println("Let agents use Docker inside runs: sudo aether-server sysbox install (or Settings → Server in the dashboard)")
		}
	}
	warnShadowed(os.Stderr, replaced[0])
	if *noApp {
		return nil
	}
	// replaced[0] is this CLI's own path with its symlinks resolved, which
	// is where the new binary now sits.
	return rebuildDesktopApp(replaced[0])
}

// checkServerSchema refuses tag on a server host - a Linux box with
// aether-server beside self, which selfupdate.Update replaces too - when
// the release's server does not know the schema this build's server
// migrates the database to: it could not open the database.
func checkServerSchema(ctx context.Context, base, tag, self string) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(self), "aether-server")); err != nil {
		return nil
	}
	m, err := selfupdate.FetchManifestFrom(ctx, base, tag)
	if err != nil {
		return err
	}
	if m.StoreSchema < store.SchemaVersion() {
		return fmt.Errorf("refusing to install aether-server %s: it knows store schema %d and this build's server migrates the database to schema %d",
			tag, m.StoreSchema, store.SchemaVersion())
	}
	return nil
}

// linkedServer is what the server-first check needs from the linked
// server; reachable is false when there is no link or no answer.
type linkedServer struct {
	reachable bool
	version   string
	admin     bool
	capable   bool
	manual    []string
}

// probeLinkedServer asks the linked server for its version, this member's
// role, and whether it can update itself. Anything that stops the answer
// - no link, an unreachable server - reports it unreachable: the client
// update never depends on the server.
func probeLinkedServer(ctx context.Context) linkedServer {
	cfg, err := cli.Load()
	if err != nil {
		return linkedServer{}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := cli.DialContext(ctx, cfg)
	if err != nil {
		return linkedServer{}
	}
	defer func() { _ = conn.Close() }()
	// Calls take no context; closing the connection is what ends one
	// stalled past the timeout.
	defer context.AfterFunc(ctx, func() { _ = conn.Close() })()
	c, err := conn.Control()
	if err != nil {
		return linkedServer{}
	}
	defer func() { _ = c.Close() }()
	var info protocol.ServerInfoResult
	if err := c.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil {
		return linkedServer{}
	}
	out := linkedServer{reachable: true, version: info.ServerVersion, admin: info.Member.Role == string(domain.RoleAdmin)}
	var status protocol.ServerUpdateStatusResult
	if err := c.Call(protocol.MethodServerUpdateStatus, struct{}{}, &status); err == nil {
		out.capable, out.manual = status.Capable, status.ManualCommands
	}
	return out
}

// matchServer is the release to install with no --version when the linked
// server runs a newer release than this client: exactly the server's, so
// the client catches up without moving ahead of it. Empty means the latest
// release.
func matchServer(server linkedServer) string {
	core := version.ReleaseCore(server.version)
	if server.reachable && selfupdate.Behind(version.Version, core) {
		return core
	}
	return ""
}

// serverFirst refuses to move this client past a linked server that its
// admin can update: the server goes first, and only to a release every
// client can install. A member who cannot update the server is told who
// can and the install goes ahead, as it does when the server is
// unreachable or already current.
func serverFirst(w io.Writer, target string, server linkedServer) error {
	if !server.reachable || !selfupdate.Behind(server.version, target) {
		return nil
	}
	switch {
	case server.admin && server.capable:
		return fmt.Errorf("update the server first: aether server update (the server runs %s and this installs %s; --force skips this check)",
			server.version, target)
	case server.admin:
		_, _ = fmt.Fprintf(w, "the server runs %s; update it first on the server host: %s\n", server.version, strings.Join(server.manual, ", then "))
	default:
		_, _ = fmt.Fprintf(w, "the server runs %s; ask a server admin to run: aether server update\n", server.version)
	}
	return nil
}

func dockerRuntimes(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{json .Runtimes}}").Output()
	return string(out), err
}

// offerSysbox is true on a Debian or Ubuntu host whose Docker answers and
// lacks the sysbox-runc runtime.
func offerSysbox(ctx context.Context, osRelease string, runtimes func(context.Context) (string, error)) bool {
	data, err := os.ReadFile(osRelease)
	if err != nil {
		return false
	}
	var debian bool
	for _, line := range strings.Split(string(data), "\n") {
		key, value, _ := strings.Cut(line, "=")
		if key == "ID" || key == "ID_LIKE" {
			debian = debian || slices.ContainsFunc(strings.Fields(strings.Trim(value, `"`)), func(id string) bool {
				return id == "debian" || id == "ubuntu"
			})
		}
	}
	if !debian {
		return false
	}
	out, err := runtimes(ctx)
	return err == nil && !strings.Contains(out, "sysbox-runc")
}

// reportBinary names the binary a no-op update looked at. Without it,
// "already on the latest release" reads as an answer about the aether the
// shell runs, which sudo and PATH order can make a different file.
func reportBinary() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate this binary: %w", err)
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return fmt.Errorf("resolve this binary: %w", err)
	}
	fmt.Printf("this binary is %s\n", self)
	warnShadowed(os.Stderr, self)
	return nil
}

func warnShadowed(w io.Writer, self string) {
	onPath, err := exec.LookPath("aether")
	if err != nil {
		return
	}
	a, errA := os.Stat(onPath)
	b, errB := os.Stat(self)
	if errA != nil || errB != nil || os.SameFile(a, b) {
		return
	}
	_, _ = fmt.Fprintf(w, "warning: aether in PATH is %s, a different binary; update it with: %s update\n", onPath, shellquote.Quote(onPath))
}

// reportCheck prints the release check and exits 0 whatever it says: a
// caller polling for an update wants an answer, not an exit status.
func reportCheck(ctx context.Context, asJSON bool) error {
	got, err := selfupdate.DefaultChecker().Check(ctx)
	if err != nil {
		return err
	}
	if asJSON {
		line, err := json.Marshal(got)
		if err != nil {
			return err
		}
		fmt.Println(string(line))
		return nil
	}
	printCheck(got)
	return nil
}

func printCheck(got selfupdate.Check) {
	switch {
	case got.Disabled:
		fmt.Printf("release checks are off: %s is set (running %s)\n", selfupdate.OptOutEnv, version.String())
	case got.Dev:
		fmt.Printf("dev build (%s); releases are not checked\n", version.String())
	case got.UpdateAvailable && got.Packaged != "":
		fmt.Printf("update available: %s (running %s); update the %s\n", got.Latest, version.String(), got.Packaged)
	case got.UpdateAvailable:
		fmt.Printf("update available: %s (running %s); run: aether update\n", got.Latest, version.String())
	case got.Latest == "":
		fmt.Printf("running %s; no signed release is published yet\n", version.String())
	case got.Latest == version.Version:
		fmt.Printf("already on the latest release %s\n", got.Latest)
	default:
		// Ahead of the newest release, or a version neither side can
		// order. Claiming "already on the latest" would be a lie in both
		// cases, so both tags are printed and the reader decides.
		fmt.Printf("running %s; the latest release is %s\n", version.String(), got.Latest)
	}
}
