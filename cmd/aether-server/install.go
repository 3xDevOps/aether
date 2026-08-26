package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/3xDevOps/Aether/internal/serversetup"
)

func install(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	configPath := fs.String("config", serversetup.DefaultConfigPath, "options file to write")
	unitPath := fs.String("unit", serversetup.UnitPath, "systemd unit file to write")
	force := fs.Bool("force", false, "overwrite an existing config file")
	serveFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireRoot(); err != nil {
		return err
	}
	return writeAndReport(os.Stdout, *unitPath, *configPath, installValues(fs), *force)
}

// installValues collects the serve options the operator actually named.
// Options left alone stay out of the file so they keep tracking the
// binary's defaults across upgrades rather than being frozen at today's
// values. The flags that select where to write are not options themselves.
func installValues(fs *flag.FlagSet) map[string]string {
	values := map[string]string{}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "config", "unit", "force":
		default:
			values[f.Name] = f.Value.String()
		}
	})
	return values
}

// writeAndReport writes the unit and config, then prints the commands that
// activate them. It never runs systemctl itself: installing must not restart
// a live server as a side effect (this is the `aether daemon install`
// convention - render and tell).
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
	_, _ = fmt.Fprintf(w, "\nactivate it with:\n  %s\n", serversetup.ActivateCommand)
	_, _ = fmt.Fprintf(w, "\nchange options later with:\n  aether-server config set <key> <value>\n")
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
