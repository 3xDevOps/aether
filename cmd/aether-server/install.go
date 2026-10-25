package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/disk"
	edgeagent "github.com/3xDevOps/Aether/internal/edge/agent"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/serversetup"
	"github.com/3xDevOps/Aether/internal/shellquote"
	"golang.org/x/term"
)

func install(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	configPath := fs.String("config", serversetup.DefaultConfigPath, "options file to write")
	unitPath := fs.String("unit", serversetup.UnitPath, "systemd unit file to write")
	force := fs.Bool("force", false, "overwrite an existing config file")
	sysbox := fs.Bool("sysbox", true, "install the Sysbox runtime so runs get Docker inside, when this host can and does not have it")
	serveFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	values := installValues(fs)
	if err := requireEdgeAccess(values); err != nil {
		return err
	}
	if err := requireRoot(); err != nil {
		return err
	}
	if !*force {
		if err := refuseKeptChanges(*configPath, values); err != nil {
			return err
		}
	}
	return writeInstall(os.Stdout, *unitPath, *configPath, values, *force, *sysbox)
}

// Install keeps an existing config file, so an option a rerun names and
// the file sets otherwise would be dropped without a word.
func refuseKeptChanges(configPath string, values map[string]string) error {
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	options, err := configuredOptions(configPath)
	if err != nil {
		return err
	}
	var sets []string
	for _, key := range slices.Sorted(maps.Keys(values)) {
		if options.Lookup(key).Value.String() != values[key] {
			sets = append(sets, fmt.Sprintf("  %saether-server config set %s %s%s", sudoPrefix(), key, shellquote.Quote(values[key]), configFlag(configPath)))
		}
	}
	if len(sets) == 0 {
		return nil
	}
	return fmt.Errorf("%s exists and sets other values than you named; install keeps an existing config file, so nothing was written. Apply them with:\n%s\nor rewrite the file from these flags alone by adding --force",
		configPath, strings.Join(sets, "\n"))
}

// A command run through sudo prints commands its user can paste.
func sudoPrefix() string {
	if os.Getenv("SUDO_USER") != "" {
		return "sudo "
	}
	return ""
}

func configFlag(configPath string) string {
	if configPath == serversetup.DefaultConfigPath {
		return ""
	}
	return " --config " + shellquote.Quote(configPath)
}

// writeInstall carries on when Docker does not answer, since automation
// may install it next, but leaves Sysbox out: its installer needs Docker.
func writeInstall(w io.Writer, unitPath, configPath string, values map[string]string, force, sysbox bool) error {
	docker := checkDocker(w)
	if err := writeAndReport(w, unitPath, configPath, values, force); err != nil {
		return err
	}
	if sysbox {
		switch {
		case !docker:
			_, _ = fmt.Fprintln(w, "\n"+sysboxNeedsDocker)
		case !sysboxOn():
			_, _ = fmt.Fprintln(w)
			installSysbox(w)
		}
	}
	options, err := configuredOptions(configPath)
	if err != nil {
		return err
	}
	warnHost(w, options.Lookup("data-dir").Value.String())
	if _, err := reportEdge(w, options, false); err != nil {
		return err
	}
	return printNextSteps(w, configPath, options, "", docker)
}

// The least a run needs to start, from internal/scheduler/capacity.go: 2
// GiB of memory kept free plus 512 MiB for the starting run, and the
// smallest disk reserve, 5 GiB, plus 1 GiB. docs/install.md states them.
const (
	minAvailableMemory = 5 << 29
	minFreeDisk        = 6 << 30
)

// warnHost warns rather than fails: automation may install what is missing
// next.
func warnHost(w io.Writer, dataDir string) {
	var lacks []string
	for _, tool := range []string{"git", "ssh-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			lacks = append(lacks, tool+" is not installed")
		}
	}
	// sd_booted(3): systemd is the init system when this directory exists.
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		lacks = append(lacks, "systemd is not running, so the unit cannot start")
	}
	if available, ok := memAvailable(); ok && available < minAvailableMemory {
		lacks = append(lacks, fmt.Sprintf("%s of memory available, below the %s minimum", gib(available), gib(minAvailableMemory)))
	}
	dir := dataDir
	for {
		if _, err := os.Stat(dir); err == nil || filepath.Dir(dir) == dir {
			break
		}
		dir = filepath.Dir(dir)
	}
	if free, err := disk.Free(dir); err == nil && free < minFreeDisk {
		lacks = append(lacks, fmt.Sprintf("%s free disk for %s, below the %s minimum", gib(free), dataDir, gib(minFreeDisk)))
	}
	if len(lacks) > 0 {
		_, _ = fmt.Fprintf(w, "\nwarning: %s (docs/install.md#server-prerequisites)\n", strings.Join(lacks, "; "))
	}
}

func memAvailable() (uint64, bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	for line := range strings.Lines(string(data)) {
		if rest, ok := strings.CutPrefix(line, "MemAvailable:"); ok {
			kb, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
			return kb << 10, err == nil
		}
	}
	return 0, false
}

func gib(bytes uint64) string {
	return strconv.FormatFloat(float64(bytes)/(1<<30), 'f', 1, 64) + " GiB"
}

// requireEdgeAccess refuses to turn the edge on without a policy: who
// may reach the server through it is the owner's choice, not a default.
func requireEdgeAccess(values map[string]string) error {
	if values["edge-url"] != "" && values["edge-access"] == "" {
		return errors.New("--edge-url turns the edge on and needs --edge-access: " +
			"account (signing in with GitHub is enough) or approved-devices (each new device waits until a person approves it)")
	}
	return nil
}

// installValues collects the serve options the operator actually named.
// Options left alone stay out of the file so they keep tracking the
// binary's defaults across upgrades rather than being frozen at today's
// values. The flags that select where to write are not options themselves.
func installValues(fs *flag.FlagSet) map[string]string {
	values := map[string]string{}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "config", "unit", "force", "sysbox":
		default:
			values[f.Name] = f.Value.String()
		}
	})
	return values
}

// writeAndReport writes the unit and config and says what it wrote.
func writeAndReport(w io.Writer, unitPath, configPath string, values map[string]string, force bool) error {
	res, err := serversetup.Install(unitPath, configPath, values, force)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "wrote %s\n", res.UnitPath)
	if res.ConfigSkipped {
		_, _ = fmt.Fprintf(w, "kept %s (already exists; pass --force to overwrite)\n", res.ConfigPath)
	} else {
		_, _ = fmt.Fprintf(w, "wrote %s\n", res.ConfigPath)
	}
	_, _ = fmt.Fprintf(w, "\nchange options later with:\n  aether-server config set <key> <value>\n")
	return nil
}

// configuredOptions reads the config file back into the serve options, so
// what setup and install report matches the file on disk even when an
// existing one was kept.
func configuredOptions(configPath string) (*flag.FlagSet, error) {
	values, err := serversetup.Load(configPath)
	if err != nil {
		return nil, err
	}
	fs := serveFlagSet()
	if _, err := serversetup.Apply(fs, values); err != nil {
		return nil, err
	}
	return fs, nil
}

// printNextSteps prints the commands left for the person to run. Setup
// and install print it last, so the Sysbox installer's output cannot bury
// it. It never runs systemctl itself: installing must not restart a live
// server as a side effect (this is the `aether daemon install` convention
// - render and tell). claimCode is the code reportEdge issued, or "";
// docker is whether the Docker daemon answered.
func printNextSteps(w io.Writer, configPath string, options *flag.FlagSet, claimCode string, docker bool) error {
	sudo := sudoPrefix()
	show := func(s string) string { return s }
	// NO_COLOR: https://no-color.org
	if f, ok := w.(*os.File); ok && term.IsTerminal(int(f.Fd())) && os.Getenv("NO_COLOR") == "" {
		show = func(s string) string { return "\x1b[1m" + attribution.Sprint("#f58231", s) }
	}
	edgeURL := options.Lookup("edge-url").Value.String()
	var owner *edgeproto.Account
	if edgeURL != "" && claimCode == "" {
		var err error
		if owner, err = edgeagent.OpenState(options.Lookup("data-dir").Value.String()).Owner(); err != nil {
			return err
		}
	}
	claim := func(code string) string {
		commands := claimCommands(edgeURL, code)
		return show(commands[0]) + "\n       " + show(commands[1])
	}
	var reach []string
	switch {
	case claimCode != "":
		reach = []string{"on your computer, open the claim link above with the Aether app, or paste it\n" +
			"     into the app's Connect page; in a terminal:\n       " + claim(claimCode)}
	case edgeURL != "" && owner == nil:
		reach = []string{
			"on this machine, print a claim code:\n       " + show(sudo+"aether-server edge claim-code"+configFlag(configPath)),
			"on your computer, sign in and claim the server with that code:\n       " + claim("<code>"),
		}
	default:
		addr := options.Lookup("addr").Value.String()
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return fmt.Errorf("addr %q: %w", addr, err)
		}
		reach = []string{"on your computer (the first link on a fresh server becomes admin):\n       " +
			show("aether link <this-host>:"+port)}
	}
	steps := append([]string{
		"start the server on this machine:\n       " + show(strings.ReplaceAll(serversetup.ActivateCommand, "systemctl", sudo+"systemctl")),
	}, reach...)
	if !docker {
		steps = slices.Insert(steps, 0, "install or start Docker on this machine:\n       "+show(dockerInstall))
	}
	if edgeURL != "" {
		steps = append(steps, "to run agents from ChatGPT or Claude, on that computer:\n       "+show("aether mcp"))
	}
	_, _ = fmt.Fprintf(w, "\n%s\n", show("next:"))
	for i, step := range steps {
		_, _ = fmt.Fprintf(w, "  %d. %s\n", i+1, step)
	}
	return nil
}

// A claim code names no edge, and the client falls back to the project's,
// so a server on another edge needs --edge on both commands.
func claimCommands(edgeURL, code string) []string {
	edge := ""
	if edgeURL != edgeagent.DefaultURL {
		edge = " --edge " + edgeURL
	}
	return []string{"aether login" + edge, "aether link --claim " + code + edge}
}

// requireRoot refuses before anything is written rather than leaving a
// half-installed /etc: both the unit and the config live in root-owned
// directories.
func requireRoot() error {
	if os.Geteuid() != 0 {
		return errors.New("writing the unit and the config file needs root; re-run with sudo")
	}
	return nil
}
