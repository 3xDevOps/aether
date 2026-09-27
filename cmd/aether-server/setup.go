package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/3xDevOps/Aether/internal/edgeagent"
	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/3xDevOps/Aether/internal/reachability"
	"github.com/3xDevOps/Aether/internal/serversetup"
	"golang.org/x/term"
)

func setup(args []string) error {
	// Notify overrides inherited ignored signals while a prompt is blocked.
	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		defer close(done)
		if sig, ok := <-signals; ok {
			switch sig {
			case os.Interrupt:
				os.Exit(130)
			case syscall.SIGTERM:
				os.Exit(143)
			}
		}
	}()
	defer func() {
		signal.Stop(signals)
		close(signals)
		<-done
	}()

	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	configPath := fs.String("config", serversetup.DefaultConfigPath, "options file to write")
	unitPath := fs.String("unit", serversetup.UnitPath, "systemd unit file to write")
	force := fs.Bool("force", false, "overwrite an existing config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("setup asks questions and needs a terminal; use `aether-server install` with flags instead")
	}
	// Refuse before the questions rather than after them: nobody should
	// answer six prompts only to be told the write needs sudo.
	if err := requireRoot(); err != nil {
		return err
	}
	_, tailscaled := os.Stat(reachability.DefaultTailscaledSocket)
	values, err := askServerOptions(os.Stdout, os.Stdin, *configPath, tailscaled == nil)
	if err != nil {
		return err
	}
	if values == nil {
		_, _ = fmt.Fprintln(os.Stdout, "nothing written")
		return nil
	}
	if err := writeAndReport(os.Stdout, *unitPath, *configPath, values, *force); err != nil {
		return err
	}
	return reportEdge(os.Stdout, *configPath)
}

// reportEdge prints what a person needs to reach the server through the
// edge the written config names: the server id, the pinned edge key, and
// a claim code while the server has no owner. It reads the config back
// because an existing file is kept without --force.
func reportEdge(w io.Writer, configPath string) error {
	values, err := serversetup.Load(configPath)
	if err != nil {
		return err
	}
	fs := serveFlagSet()
	if _, err = serversetup.Apply(fs, values); err != nil {
		return err
	}
	edgeURL := fs.Lookup("edge-url").Value.String()
	if edgeURL == "" {
		return nil
	}
	dataDir := fs.Lookup("data-dir").Value.String()
	id, err := serverID(dataDir)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "\nedge: %s\nserver id: %s\n", edgeURL, id)
	state := edgeagent.OpenState(dataDir)
	pinned, err := state.PinnedKey()
	if err != nil {
		return err
	}
	if pinned != nil {
		_, _ = fmt.Fprintf(w, "edge key: %s\n", edgeproto.EdgeKeyFingerprint(pinned))
	} else {
		_, _ = fmt.Fprintln(w, "edge key: pinned when the server first connects; `aether-server edge status` shows it")
	}
	owner, err := state.Owner()
	if err != nil {
		return err
	}
	if owner != nil {
		_, _ = fmt.Fprintf(w, "owner: %s\n", describeAccount(*owner))
		return nil
	}
	code, expires, err := state.IssueClaimCode(id, time.Now())
	if err != nil {
		return err
	}
	printClaimCode(w, edgeURL, code, expires)
	return nil
}

// askServerOptions walks the operator through the handful of options a
// server install actually chooses, seeding each default from the existing
// config file so re-running setup is not destructive. tailnet says whether
// tailscaled is on this host, which is what makes the dashboard port
// worth offering. It returns a nil map when the operator declines the
// summary.
func askServerOptions(w io.Writer, in io.Reader, configPath string, tailnet bool) (map[string]string, error) {
	current, err := serversetup.Load(configPath)
	if err != nil {
		return nil, err
	}
	p := &prompter{w: w, lines: bufio.NewReader(in), options: serveFlagSet()}
	// An existing config answers first, then the packaged service posture,
	// then the flag's own default, so re-running setup is not destructive
	// and a first run offers what a service install would have used.
	service := serversetup.ServiceDefaults()
	def := func(key string) string {
		if v, ok := current[key]; ok {
			return v
		}
		if v, ok := service[key]; ok {
			return v
		}
		return p.options.Lookup(key).DefValue
	}

	values := map[string]string{}
	values["addr"] = p.ask("addr", "SSH listen address", def("addr"))
	values["data-dir"] = p.ask("data-dir", "Data directory", def("data-dir"))

	_, _ = fmt.Fprintln(w, "\nWith tailnet auto-join on, anyone already on your tailnet becomes an")
	_, _ = fmt.Fprintln(w, "approved member on first connect, with no admin approving them.")
	values["tailnet-auto-join"] = p.ask("tailnet-auto-join",
		"Auto-approve tailnet identities (true/false)", def("tailnet-auto-join"))

	_, _ = fmt.Fprintln(w, "\nRequiring a pubkey on top of tailnet identity means a member must have")
	_, _ = fmt.Fprintln(w, "linked a key before their tailnet identity is trusted.")
	values["tailnet-require-key"] = p.ask("tailnet-require-key",
		"Also require pubkey verification on tailnet connections (true/false)", def("tailnet-require-key"))

	// The dashboard cannot present a key, so it is only offered where it
	// could start: tailscaled on this host and no key requirement. The
	// default is on there, because a phone reaching the dashboard is the
	// point of putting the server on a tailnet, and a tailnet without
	// HTTPS certificates fails the first start with the fix in the error.
	if tailnet && values["tailnet-require-key"] != "true" {
		webDefault := def("web-port")
		if _, ok := current["web-port"]; !ok {
			webDefault = "443"
		}
		_, _ = fmt.Fprintln(w, "\nThe server can host the dashboard over HTTPS on its tailnet address, so a")
		_, _ = fmt.Fprintln(w, "phone or any tailnet device opens https://<this host's MagicDNS name>/ with")
		_, _ = fmt.Fprintln(w, "no token. The tailnet needs MagicDNS and HTTPS certificates enabled; 0 keeps")
		_, _ = fmt.Fprintln(w, "the server SSH-only.")
		values["web-port"] = p.ask("web-port", "Dashboard HTTPS port on the tailnet (0 = off)", webDefault)
	}

	askEdge(p, current, tailnet, values)

	if p.err != nil {
		return nil, p.err
	}
	_, _ = fmt.Fprintf(w, "\n%s will hold:\n\n%s\n", configPath, serversetup.Render(values))
	if !p.confirm("Write it", true) || p.err != nil {
		return nil, p.err
	}
	return values, nil
}

// askEdge sets edge-url. Without tailscaled the edge is the only route a
// laptop or phone has to the server, so it is on and setup says so; with
// tailscaled it is offered. An edge-url the operator already set is kept
// or offered as the answer.
func askEdge(p *prompter, current map[string]string, tailnet bool, values map[string]string) {
	configured, set := current["edge-url"]
	if set && configured == "" && !tailnet {
		_, _ = fmt.Fprintf(p.w, "\nThe edge is off (edge-url is empty) and Tailscale is not installed, so members\nreach this server only by SSH to %s.\n", values["addr"])
		values["edge-url"] = ""
		return
	}
	edgeURL := configured
	if edgeURL == "" {
		edgeURL = edgeagent.DefaultURL
	}
	if tailnet {
		_, _ = fmt.Fprintf(p.w, "\nThe server can also be reached through the edge at %s, for devices\nthat are not on your tailnet.\n", edgeURL)
	} else {
		_, _ = fmt.Fprintf(p.w, "\nTailscale is not installed, so this server will be reached through the edge at\n%s.\n", edgeURL)
	}
	_, _ = fmt.Fprintln(p.w, "SSH runs end to end through the edge and the dashboard's TLS ends on this server,")
	_, _ = fmt.Fprintln(p.w, "so the edge cannot read either; it sees this server's id, who signs in, device")
	_, _ = fmt.Fprintln(p.w, "names, client IP addresses, and when and how much traffic flows.")
	if !tailnet {
		_, _ = fmt.Fprintln(p.w, `Turn it off with: aether-server config set edge-url ""`)
		values["edge-url"] = edgeURL
		return
	}
	values["edge-url"] = ""
	if p.confirm("Reach this server through the edge too", configured != "") {
		values["edge-url"] = edgeURL
	}
}

// prompter reads answers off one line-oriented stream, latching the first
// read error so a caller checks it once at the end instead of after every
// question.
type prompter struct {
	w       io.Writer
	lines   *bufio.Reader
	options *flag.FlagSet
	err     error
	// eof stops ask from re-asking once the input is exhausted, which
	// would otherwise spin forever on a default the options reject.
	eof bool
}

// ask shows def, returns it when the operator just presses enter, and
// re-asks when the answer is not a value the named option accepts.
func (p *prompter) ask(option, label, def string) string {
	for p.err == nil && !p.eof {
		answer := p.readLine(label, def)
		if answer == "" || p.err != nil || p.eof {
			return answer
		}
		if err := p.options.Set(option, answer); err != nil {
			_, _ = fmt.Fprintf(p.w, "  %v; try again\n", err)
			continue
		}
		return answer
	}
	return def
}

// confirm asks a yes/no question, showing def as the word an empty answer
// takes.
func (p *prompter) confirm(label string, def bool) bool {
	word := "no"
	if def {
		word = "yes"
	}
	switch strings.ToLower(p.readLine(label+" (yes/no)", word)) {
	case "y", "yes", "true":
		return true
	case "n", "no", "false":
		return false
	default:
		return def
	}
}

func (p *prompter) readLine(label, def string) string {
	if p.err != nil || p.eof {
		return def
	}
	_, _ = fmt.Fprintf(p.w, "%s [%s]: ", label, def)
	line, err := p.lines.ReadString('\n')
	switch {
	case err == io.EOF:
		p.eof = true
	case err != nil:
		p.err = err
		return def
	}
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	return def
}
