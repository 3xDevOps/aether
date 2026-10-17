package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/3xDevOps/Aether/internal/attribution"
	edgeagent "github.com/3xDevOps/Aether/internal/edge/agent"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/serversetup"
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
	if err := writeAndReport(os.Stdout, *unitPath, *configPath, values, *force); err != nil {
		return err
	}
	if *sysbox && !sysboxOn() {
		_, _ = fmt.Fprintln(os.Stdout)
		installSysbox(os.Stdout)
	}
	options, err := configuredOptions(*configPath)
	if err != nil {
		return err
	}
	return printNextSteps(os.Stdout, *configPath, options, "")
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
// - render and tell). claimCode is the code reportEdge issued, or "".
func printNextSteps(w io.Writer, configPath string, options *flag.FlagSet, claimCode string) error {
	sudo := ""
	if os.Getenv("SUDO_USER") != "" {
		sudo = "sudo "
	}
	show := func(s string) string { return s }
	// NO_COLOR: https://no-color.org
	if f, ok := w.(*os.File); ok && term.IsTerminal(int(f.Fd())) && os.Getenv("NO_COLOR") == "" {
		show = func(s string) string { return "\x1b[1m" + attribution.Sprint("#f58231", s) }
	}
	edgeOn := options.Lookup("edge-url").Value.String() != ""
	var owner *edgeproto.Account
	if edgeOn && claimCode == "" {
		var err error
		if owner, err = edgeagent.OpenState(options.Lookup("data-dir").Value.String()).Owner(); err != nil {
			return err
		}
	}
	var reach string
	switch {
	case claimCode != "":
		reach = "on your computer, open the claim link above with the Aether app, or paste it\n" +
			"     into the app's Connect page; in a terminal:\n       " + show("aether link --claim "+claimCode)
	case edgeOn && owner == nil:
		claim := sudo + "aether-server edge claim-code"
		if configPath != serversetup.DefaultConfigPath {
			claim += " --config '" + strings.ReplaceAll(configPath, "'", `'\''`) + "'"
		}
		reach = "on this machine, print a claim link to open on your computer:\n       " + show(claim)
	default:
		addr := options.Lookup("addr").Value.String()
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return fmt.Errorf("addr %q: %w", addr, err)
		}
		reach = "on your computer (the first link on a fresh server becomes admin):\n       " +
			show("aether link <this-host>:"+port)
	}
	_, _ = fmt.Fprintf(w, "\n%s\n  1. start the server on this machine:\n       %s\n  2. %s\n", show("next:"),
		show(strings.ReplaceAll(serversetup.ActivateCommand, "systemctl", sudo+"systemctl")), reach)
	return nil
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
