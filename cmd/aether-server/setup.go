package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	edgeagent "github.com/3xDevOps/Aether/internal/edge/agent"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/reachability"
	"github.com/3xDevOps/Aether/internal/serversetup"
	"golang.org/x/term"
)

func setup(args []string) error {
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
	docker := checkDocker(os.Stdout)
	values, sysbox, err := askSetup(*configPath, docker)
	if err != nil {
		return err
	}
	if values == nil {
		_, _ = fmt.Fprintln(os.Stdout, "nothing written")
		return nil
	}
	return writeSetup(os.Stdout, *unitPath, *configPath, values, *force, sysbox, docker)
}

// askSetup asks whether to go on without Docker when docker is false, then
// setup's questions, and whether to install Sysbox. A signal at a prompt
// ends setup with nothing written; the handler ends with the questions so
// that installSysbox can take Ctrl-C for itself.
func askSetup(configPath string, docker bool) (map[string]string, bool, error) {
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

	p := newPrompter(os.Stdout, os.Stdin)
	if !docker {
		if err := askWithoutDocker(p); err != nil {
			return nil, false, err
		}
	}
	var tailscaled *reachability.Tailscale
	if _, err := os.Stat(reachability.DefaultTailscaledSocket); err == nil {
		tailscaled = reachability.NewTailscale("")
	}
	values, err := askServerOptions(p, configPath, tailscaled)
	if values == nil || err != nil {
		return nil, false, err
	}
	return values, askSysbox(p, docker), nil
}

func askWithoutDocker(p *prompter) error {
	yes := p.confirm("Continue without Docker", false)
	switch {
	case p.err != nil:
		return p.err
	case !yes:
		return errors.New("nothing written; install or start Docker, then run setup again")
	}
	return nil
}

// writeSetup issues the claim code after the slow Sysbox install, so the
// code's 30 minutes start when the person can use it. Without Docker the
// server cannot start, so it issues none and next: says how to get one.
func writeSetup(w io.Writer, unitPath, configPath string, values map[string]string, force, sysbox, docker bool) error {
	if err := writeAndReport(w, unitPath, configPath, values, force); err != nil {
		return err
	}
	if sysbox {
		_, _ = fmt.Fprintln(w)
		installSysbox(w)
	}
	options, err := configuredOptions(configPath)
	if err != nil {
		return err
	}
	claimCode, err := reportEdge(w, options, docker)
	if err != nil {
		return err
	}
	return printNextSteps(w, configPath, options, claimCode, docker)
}

func askSysbox(p *prompter, docker bool) bool {
	switch {
	case p.err != nil:
		return false
	case !docker:
		_, _ = fmt.Fprintln(p.w, "\n"+sysboxNeedsDocker)
		return false
	case sysboxOn():
		return false
	}
	_, _ = fmt.Fprintln(p.w)
	return p.confirm("Let agents use Docker inside their runs? This installs the Sysbox runtime (Ubuntu/Debian)", true) && p.err == nil
}

// reportEdge prints what a person needs to reach the server through the
// edge the options name: the server id, the pinned edge key, and, with
// claim set, a claim link while the server has no owner. It returns the
// claim code inside that link, or "" when it issued none.
func reportEdge(w io.Writer, options *flag.FlagSet, claim bool) (string, error) {
	edgeURL := options.Lookup("edge-url").Value.String()
	if edgeURL == "" {
		return "", nil
	}
	dataDir := options.Lookup("data-dir").Value.String()
	id, err := serverID(dataDir)
	if err != nil {
		return "", err
	}
	_, _ = fmt.Fprintf(w, "\nedge: %s\nedge access: %s\nserver id: %s\n", edgeURL, options.Lookup("edge-access").Value, id)
	state := edgeagent.OpenState(dataDir)
	pinned, err := state.PinnedKey()
	if err != nil {
		return "", err
	}
	if pinned != nil {
		_, _ = fmt.Fprintf(w, "edge key: %s\n", edgeproto.EdgeKeyFingerprint(pinned))
	} else {
		_, _ = fmt.Fprintln(w, "edge key: pinned when the server first connects; `aether-server edge status` shows it")
	}
	owner, err := state.Owner()
	if err != nil {
		return "", err
	}
	if owner != nil {
		_, _ = fmt.Fprintf(w, "owner: %s\n", describeAccount(*owner))
		return "", nil
	}
	if !claim {
		return "", nil
	}
	return issueClaimCode(w, state, edgeURL, id, "", time.Now())
}

// askServerOptions walks the operator through the handful of options a
// server install actually chooses, seeding each default from the existing
// config file so re-running setup is not destructive. tailscaled is the
// LocalAPI client when tailscaled is on this host, which is what makes the
// tailnet questions worth asking, and nil otherwise. It returns a nil map
// when the operator declines the summary.
func askServerOptions(p *prompter, configPath string, tailscaled *reachability.Tailscale) (map[string]string, error) {
	w := p.w
	current, err := serversetup.Load(configPath)
	if err != nil {
		return nil, err
	}
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

	// Each config a yes writes has the edge off, a web-port or auto-join on,
	// except one requiring a key with the edge on, which is what a no writes.
	_, webPort := current["web-port"]
	autoJoin, _ := strconv.ParseBool(current["tailnet-auto-join"])
	wasTailnet := len(current) > 0 && (current["edge-url"] == "" || webPort || autoJoin)
	tailnet := tailscaled != nil && askTailnet(p, tailscaled, wasTailnet)
	switch {
	case tailnet:
		_, _ = fmt.Fprintln(w, "\nWith tailnet auto-join on, anyone already on your tailnet becomes an")
		_, _ = fmt.Fprintln(w, "approved member on first connect, with no admin approving them.")
		values["tailnet-auto-join"] = p.ask("tailnet-auto-join",
			"Auto-approve tailnet identities (true/false)", def("tailnet-auto-join"))

		_, _ = fmt.Fprintln(w, "\nRequiring a pubkey on top of tailnet identity means a member must have")
		_, _ = fmt.Fprintln(w, "linked a key before their tailnet identity is trusted.")
		// A keyed tailnet server's config can match what a no writes, so a
		// saved key requirement is offered back: Enter never drops it.
		values["tailnet-require-key"] = p.ask("tailnet-require-key",
			"Also require pubkey verification on tailnet connections (true/false)", def("tailnet-require-key"))

		// The dashboard cannot present a key, so it is only offered where it
		// could start: tailscaled on this host and no key requirement. Only a
		// first run probes for the default: on a live server the dashboard
		// itself holds the port.
		if required, _ := strconv.ParseBool(values["tailnet-require-key"]); !required {
			_, _ = fmt.Fprintln(w, "\nThe server can host the dashboard over HTTPS on its tailnet address, so a")
			_, _ = fmt.Fprintln(w, "phone or any tailnet device opens https://<this host's MagicDNS name>/ with")
			_, _ = fmt.Fprintln(w, "no token. The tailnet needs MagicDNS and HTTPS certificates enabled; 0 keeps")
			_, _ = fmt.Fprintln(w, "the server SSH-only.")
			webDefault := def("web-port")
			if _, ok := current["web-port"]; !ok {
				webDefault = strconv.Itoa(dashboardPort)
				if err := dashboardBlocked(tailscaled, dashboardPort); err != nil {
					webDefault = "0"
					_, _ = fmt.Fprintf(w, "Defaulting to 0: %v, then run: sudo aether-server config set web-port %d\n", err, dashboardPort)
				}
			}
			values["web-port"] = p.ask("web-port", "Dashboard HTTPS port on the tailnet (0 = off)", webDefault)
		}
	case tailscaled != nil:
		values["tailnet-auto-join"] = "false"
		// Without a key requirement a tailnet login still registers, and the
		// first one on a fresh server becomes its admin.
		values["tailnet-require-key"] = "true"
	default:
		values["tailnet-auto-join"] = "false"
		if v, ok := current["tailnet-require-key"]; ok {
			values["tailnet-require-key"] = v
		}
	}

	askEdge(p, current, tailscaled != nil, tailnet, values)
	if values["edge-url"] != "" {
		askEdgeAccess(p, values)
	}

	if p.err != nil {
		return nil, p.err
	}
	_, _ = fmt.Fprintf(w, "\n%s will hold:\n\n%s\n", configPath, serversetup.Render(values))
	if !p.confirm("Write it", true) || p.err != nil {
		return nil, p.err
	}
	return values, nil
}

// askTailnet asks whether people reach this server over the tailnet
// tailscaled is on, with def as the answer Enter gives.
func askTailnet(p *prompter, tailscaled *reachability.Tailscale, def bool) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tailnet := "this host's tailnet"
	if node, err := tailscaled.Self(ctx); err != nil {
		_, _ = fmt.Fprintf(p.w, "\ntailscaled is installed, but setup cannot read which tailnet it is on: %v\n", err)
	} else {
		tailnet = "the tailnet " + node.Tailnet
		_, _ = fmt.Fprintf(p.w, "\nThis host is on %s.\n", tailnet)
	}
	_, _ = fmt.Fprintln(p.w, "On a tailnet, the server signs people in by their tailnet login, with no key.")
	_, _ = fmt.Fprintln(p.w, "Answer no if Tailscale is here for something else: setup then turns tailnet")
	_, _ = fmt.Fprintln(p.w, "sign-in off and the edge on.")
	return p.confirm("Will people reach this server over "+tailnet, def)
}

// dashboardPort is the port that makes the dashboard https://<MagicDNS name>/.
const dashboardPort = 443

// dashboardBlocked says why the tailnet dashboard could not start on port,
// or nil when it could. Like servergw.Start it needs one tailnet address
// that binds, but a port in use on any address blocks, because clients on
// that address family would reach the other service. EACCES counts as
// bindable: setup runs as root, so only a non-root caller such as a test
// sees it.
func dashboardBlocked(tailscaled *reachability.Tailscale, port int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	node, err := tailscaled.Self(ctx)
	if err != nil {
		return fmt.Errorf("tailscaled did not report this node: %w; connect this host to the tailnet", err)
	}
	if len(node.CertDomains) == 0 {
		return errors.New("HTTPS certificates are off for this tailnet; enable MagicDNS and HTTPS certificates on the DNS page of the Tailscale admin console")
	}
	if len(node.Addrs) == 0 {
		return fmt.Errorf("tailscaled reports no tailnet address for %s; connect this host to the tailnet", node.DNSName)
	}
	var unbound []error
	for _, addr := range node.Addrs {
		ln, err := net.Listen("tcp", netip.AddrPortFrom(addr, uint16(port)).String())
		switch {
		case err == nil:
			_ = ln.Close()
		case errors.Is(err, syscall.EADDRINUSE):
			return fmt.Errorf("%w; answer another port such as 8443 below, or free port %d there", err, port)
		case !errors.Is(err, syscall.EACCES):
			unbound = append(unbound, err)
		}
	}
	if len(unbound) == len(node.Addrs) {
		return fmt.Errorf("%w; tailscaled must put the tailnet addresses on this host's TUN interface, which --tun=userspace-networking does not", unbound[0])
	}
	return nil
}

// askEdge sets edge-url, the one place the edge is turned on. Without a
// tailnet - no tailscaled, or a no to askTailnet - the edge is the only
// route a laptop or phone has to the server, so it is on and setup says
// what that discloses; on a tailnet it is offered, off unless the config
// already has it on. An edge-url the operator already set is kept or
// offered as the answer. An empty one is kept only without tailscaled:
// with tailscaled it is what declining the offer wrote.
func askEdge(p *prompter, current map[string]string, tailscaled, tailnet bool, values map[string]string) {
	configured, set := current["edge-url"]
	if set && configured == "" && !tailscaled {
		_, _ = fmt.Fprintf(p.w, "\nThe edge is off (edge-url is empty) and Tailscale is not installed, so members\nreach this server only by SSH to %s.\n", values["addr"])
		values["edge-url"] = ""
		return
	}
	edgeURL := configured
	if edgeURL == "" {
		edgeURL = edgeagent.DefaultURL
	}
	operator := ""
	if edgeURL == edgeagent.DefaultURL {
		operator = ", run by the Aether project"
	}
	switch {
	case tailnet:
		_, _ = fmt.Fprintf(p.w, "\nDevices that are not on your tailnet can reach this server through the edge at\n%s%s.\n", edgeURL, operator)
	case tailscaled:
		_, _ = fmt.Fprintf(p.w, "\nTailnet sign-in is off, so setup turns on the edge at %s%s.\n", edgeURL, operator)
	default:
		_, _ = fmt.Fprintf(p.w, "\nTailscale is not installed, so setup turns on the edge at %s%s.\n", edgeURL, operator)
	}
	_, _ = fmt.Fprintln(p.w, "The edge is a relay: this server and your devices both dial out to it, so")
	_, _ = fmt.Fprintln(p.w, "neither needs an open port. SSH runs end to end through it, so the edge cannot")
	_, _ = fmt.Fprintln(p.w, "read it. It sees this server's host name, host key and IP address, who signs")
	_, _ = fmt.Fprintln(p.w, "in, device names, client IP addresses, and when and how much traffic flows.")
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

const edgeAccessQuestion = `
Who may reach this server through the edge?

  1) Account access
     Signing in with GitHub is enough. People you invite start on a new
     device by signing in, and Aether creates and manages the device key.
     Access is as strong as each person's GitHub account and the edge that
     vouches for it.

  2) Approved devices
     Signing in says who someone is. It does not admit a device. Each new
     device waits until it is approved: by that person from a device they
     already use, by an admin, or by you on this machine. A taken-over
     account, or a compromised edge, cannot add a device on its own.
     Recommended for a team workspace holding code or credentials that
     must stay protected.

Both leave Tailscale and direct SSH key access as they are.
Change it on this machine: aether-server config set edge-access <account|approved-devices>
`

// askEdgeAccess sets edge-access. The question has no default: it repeats
// until the answer is 1 or 2, and running out of input is an error, so
// setup never chooses the policy for the operator.
func askEdgeAccess(p *prompter, values map[string]string) {
	_, _ = fmt.Fprint(p.w, edgeAccessQuestion)
	for p.err == nil {
		_, _ = fmt.Fprint(p.w, "\nChoose 1 or 2: ")
		line, err := p.lines.ReadString('\n')
		switch strings.TrimSpace(line) {
		case "1":
			values["edge-access"] = string(edgeproto.PolicyAccount)
			return
		case "2":
			values["edge-access"] = string(edgeproto.PolicyApprovedDevices)
			return
		}
		switch {
		case errors.Is(err, io.EOF):
			p.err = errors.New("setup: the edge needs an access policy; answer 1 or 2")
		case err != nil:
			p.err = err
		}
	}
}

func newPrompter(w io.Writer, in io.Reader) *prompter {
	return &prompter{w: w, lines: bufio.NewReader(in), options: serveFlagSet()}
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
