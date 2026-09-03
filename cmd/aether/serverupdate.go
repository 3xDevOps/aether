package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "server",
		short: "server administration: update",
		run:   runServer,
	})
}

func runServer(args []string) error {
	if len(args) < 1 || args[0] != "update" {
		return errors.New("usage: aether server <update>")
	}
	return serverUpdate(args[1:])
}

type serverUpdateOptions struct {
	status  bool
	version string
	when    string
	yes     bool
}

const serverUpdateUsage = "usage: aether server update [--status] [--version <tag>] [--when now|idle] [--cancel] [--yes]"

// parseServerUpdate parses the flags of "aether server update". --status
// and --cancel each stand alone: --status reports state rather than
// changing it, and --cancel is a shorthand for --when cancel, so combining
// either with the flags that shape an actual update is refused rather than
// silently picking a winner.
func parseServerUpdate(args []string) (serverUpdateOptions, error) {
	fs := flag.NewFlagSet("server update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	status := fs.Bool("status", false, "report the server's update state")
	version := fs.String("version", "", "release tag to install (default: latest)")
	when := fs.String("when", protocol.ServerUpdateNow, `"now" or "idle"`)
	cancel := fs.Bool("cancel", false, "cancel a pending update")
	yes := fs.Bool("yes", false, "skip the confirmation")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return serverUpdateOptions{}, errors.New(serverUpdateUsage)
	}
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })

	if *status {
		if seen["version"] || seen["when"] || seen["cancel"] || seen["yes"] {
			return serverUpdateOptions{}, errors.New("aether server update: --status cannot be combined with other flags")
		}
		return serverUpdateOptions{status: true}, nil
	}
	if *cancel {
		if seen["when"] {
			return serverUpdateOptions{}, errors.New("aether server update: --cancel and --when cannot be used together")
		}
		if seen["version"] {
			return serverUpdateOptions{}, errors.New("aether server update: --cancel and --version cannot be used together")
		}
		return serverUpdateOptions{when: protocol.ServerUpdateCancel}, nil
	}
	if *when != protocol.ServerUpdateNow && *when != protocol.ServerUpdateIdle {
		return serverUpdateOptions{}, errors.New(`aether server update: --when must be "now" or "idle"`)
	}
	return serverUpdateOptions{version: *version, when: *when, yes: *yes}, nil
}

func serverUpdate(args []string) error {
	opts, err := parseServerUpdate(args)
	if err != nil {
		return err
	}
	switch {
	case opts.status:
		return serverUpdateStatus()
	case opts.when == protocol.ServerUpdateCancel:
		return serverUpdateCancel()
	case opts.when == protocol.ServerUpdateIdle:
		return serverUpdateIdle(opts.version)
	default:
		return serverUpdateNow(opts.version, opts.yes)
	}
}

func serverUpdateStatus() error {
	return withControl(func(c *protocol.Client) error {
		var res protocol.ServerUpdateStatusResult
		if err := c.Call(protocol.MethodServerUpdateStatus, struct{}{}, &res); err != nil {
			return err
		}
		return printServerUpdateStatus(os.Stdout, res)
	})
}

// printServerUpdateStatus renders every field server.update_status
// returns. Nothing is summarized away: a manual-install server prints its
// exact commands, and a failed attempt prints its detail in full.
func printServerUpdateStatus(w io.Writer, res protocol.ServerUpdateStatusResult) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "server version\t%s\n", res.ServerVersion)
	latest := res.Latest
	if latest == "" {
		latest = "unknown"
	}
	_, _ = fmt.Fprintf(tw, "latest release\t%s\n", latest)
	available := "no"
	if res.UpdateAvailable {
		available = "yes"
	}
	_, _ = fmt.Fprintf(tw, "update available\t%s\n", available)
	if err := tw.Flush(); err != nil {
		return err
	}
	if !res.Capable {
		reason := res.Incapable
		if reason == "" {
			reason = "reason not reported"
		}
		if _, err := fmt.Fprintf(w, "\nthis server cannot update itself: %s\nrun on the server host:\n",
			printable(reason)); err != nil {
			return err
		}
		for _, cmd := range res.ManualCommands {
			if _, err := fmt.Fprintf(w, "  %s\n", cmd); err != nil {
				return err
			}
		}
	}
	if res.Pending != nil {
		if _, err := fmt.Fprintf(w, "\npending update: %s, requested by %s at %s\n",
			res.Pending.Version, res.Pending.RequestedBy, shortTime(res.Pending.RequestedAt)); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "  %s\n", waitingLine(res.Waiting)); err != nil {
			return err
		}
	}
	if res.Last != nil {
		if _, err := fmt.Fprintf(w, "\nlast attempt: %s %s at %s\n",
			res.Last.Version, res.Last.Outcome, shortTime(res.Last.At)); err != nil {
			return err
		}
		if res.Last.Detail != "" {
			if _, err := fmt.Fprintf(w, "  %s\n", printable(res.Last.Detail)); err != nil {
				return err
			}
		}
	}
	return nil
}

// waitingLine says what a pending update has not applied for. Paused runs
// are named because they look active in `aether runs` but are not holding
// anything back; leaving them out would make the count look wrong.
func waitingLine(w *protocol.ServerUpdateWaiting) string {
	if w == nil {
		return "nothing is holding it back; it applies on the next poll"
	}
	parts := make([]string, 0, 3)
	if w.Runs > 0 {
		parts = append(parts, plural(w.Runs, "run")+" still working")
	}
	if w.Shells > 0 {
		parts = append(parts, plural(w.Shells, "workspace shell")+" open")
	}
	if w.Paused > 0 {
		// Named because a paused run shows as running in `aether runs`;
		// leaving it out would make the count look wrong.
		parts = append(parts, plural(w.Paused, "paused run")+" not holding it back")
	}
	if len(parts) == 0 {
		// The server reported busy without naming anything this client
		// understands, which means it is running a newer protocol.
		return "waiting: the server did not say what for"
	}
	return "waiting: " + strings.Join(parts, ", ")
}

func serverUpdateCancel() error {
	return withControl(func(c *protocol.Client) error {
		var res protocol.ServerUpdateResult
		if err := c.Call(protocol.MethodServerUpdate, protocol.ServerUpdateParams{When: protocol.ServerUpdateCancel}, &res); err != nil {
			return err
		}
		if res.Version == "" {
			fmt.Println("no pending server update")
			return nil
		}
		fmt.Printf("cancelled the pending update %s\n", res.Version)
		return nil
	})
}

func serverUpdateIdle(version string) error {
	return withControl(func(c *protocol.Client) error {
		var res protocol.ServerUpdateResult
		if err := c.Call(protocol.MethodServerUpdate, protocol.ServerUpdateParams{Version: version, When: protocol.ServerUpdateIdle}, &res); err != nil {
			return err
		}
		fmt.Printf("scheduled %s; it applies when no run is working and no workspace shell is open\n", res.Version)
		return nil
	})
}

// serverUpdateNow counts active runs, confirms unless --yes was passed,
// then opens the event subscription before making the call (like
// envRebuild) so no event between the two can be missed.
func serverUpdateNow(version string, yes bool) error {
	cfg, err := cli.Load()
	if err != nil {
		return err
	}
	conn, err := cli.Dial(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	c, err := conn.Control()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	var rl protocol.RunListResult
	if err = c.Call(protocol.MethodRunList, protocol.RunListParams{ActiveOnly: true}, &rl); err != nil {
		return err
	}

	if !yes && term.IsTerminal(int(os.Stdout.Fd())) {
		ok, cerr := confirmServerUpdateNow(os.Stdin, os.Stdout, version, len(rl.Runs))
		if cerr != nil {
			return cerr
		}
		if !ok {
			fmt.Println("aborted")
			return nil
		}
	}

	stream, err := conn.Events(protocol.SubscribeRequest{Types: []string{string(events.TypeServerUpdate)}})
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()

	var res protocol.ServerUpdateResult
	if err = c.Call(protocol.MethodServerUpdate, protocol.ServerUpdateParams{Version: version, When: protocol.ServerUpdateNow}, &res); err != nil {
		return err
	}
	// The result already reports the applying phase, which is also the
	// first event on the feed; pass it as reported so it prints once.
	fmt.Printf("%s %s\n", res.Status, res.Version)
	return followServerUpdate(stream, os.Stdout, res.Version, events.ServerUpdateApplying)
}

// confirmServerUpdateNow prints what applying now costs and reads one line
// from r; only "y" or "yes" (either case) continues. The version may be
// unresolved when --version was not given, since the server picks the
// latest release itself - the prompt says so rather than guessing a tag.
func confirmServerUpdateNow(r io.Reader, w io.Writer, version string, activeRuns int) (bool, error) {
	target := version
	if target == "" {
		target = "the latest release"
	}
	_, _ = fmt.Fprintf(w, "updating the server to %s restarts it now.\n", target)
	if activeRuns > 0 {
		_, _ = fmt.Fprintf(w, "%s active; they keep running and the server reattaches to them.\n", plural(activeRuns, "run"))
	} else {
		_, _ = fmt.Fprintln(w, "no runs are active.")
	}
	_, _ = fmt.Fprintln(w, "attached terminals and live syncs drop and reconnect.")
	_, _ = fmt.Fprint(w, "continue? [y/N]: ")
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes", nil
}

// followServerUpdate reads server.update events until the stream ends,
// which is the server restarting onto the new binary - the expected way
// this call finishes, not a failure. The event is published once per
// workspace with no workspace filter on the subscription, so the same
// phase can arrive more than once; each is printed only the first time.
func followServerUpdate(stream io.Reader, w io.Writer, version string, reported ...events.ServerUpdatePhase) error {
	r := bufio.NewReaderSize(stream, 64<<10)
	seen := map[events.ServerUpdatePhase]bool{}
	for _, phase := range reported {
		seen[phase] = true
	}
	for {
		line, err := protocol.ReadLine(r)
		if err != nil {
			_, printErr := fmt.Fprintf(w, "connection closed: the server is restarting on %s; reconnect in a moment\n", version)
			return printErr
		}
		var ev protocol.Event
		if json.Unmarshal(line, &ev) != nil || ev.Type != string(events.TypeServerUpdate) {
			continue
		}
		var payload events.ServerUpdatePayload
		if json.Unmarshal(ev.Payload, &payload) != nil {
			continue
		}
		if seen[payload.Phase] {
			continue
		}
		seen[payload.Phase] = true
		switch payload.Phase {
		case events.ServerUpdateApplying:
			if _, err := fmt.Fprintf(w, "applying %s\n", payload.Version); err != nil {
				return err
			}
		case events.ServerUpdateRestarting:
			if _, err := fmt.Fprintf(w, "restarting on %s\n", payload.Version); err != nil {
				return err
			}
		case events.ServerUpdateFailed:
			return fmt.Errorf("server update failed: %s", payload.Detail)
		}
	}
}
