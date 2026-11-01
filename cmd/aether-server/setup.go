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
	values, sysbox, tailnetHost, err := askSetup(*configPath, docker)
	if err != nil {
		return err
	}
	if values == nil {
		_, _ = fmt.Fprintln(os.Stdout, "nothing written")
		return nil
	}
	return writeSetup(os.Stdout, *unitPath, *configPath, values, *force, sysbox, docker, tailnetHost)
}

// askSetup asks whether to go on without Docker when docker is false, then
// setup's questions, and whether to install Sysbox. It also returns this
// host's MagicDNS name, or "" without a tailnet.
// A signal at a prompt ends setup with nothing written; the handler ends
// with the questions so that installSysbox can take Ctrl-C for itself.
func askSetup(configPath string, docker bool) (map[string]string, bool, string, error) {
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
			return nil, false, "", err
		}
	}
	var tailscaled *reachability.Tailscale
	if _, err := os.Stat(reachability.DefaultTailscaledSocket); err == nil {
		tailscaled = reachability.NewTailscale("")
	}
	values, err := askServerOptions(p, configPath, tailscaled)
	if values == nil || err != nil {
		return nil, false, "", err
	}
	tailnetHost := ""
	if tailscaled != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if node, err := tailscaled.Self(ctx); err == nil {
			tailnetHost = node.DNSName
		}
	}
	return values, askSysbox(p, docker), tailnetHost, nil
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
func writeSetup(w io.Writer, unitPath, configPath string, values map[string]string, force, sysbox, docker bool, tailnetHost string) error {
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
	return printNextSteps(w, configPath, options, claimCode, docker, tailnetHost)
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
	return p.confirm("Let agents use Docker inside their sessions? This installs the Sysbox runtime (Ubuntu/Debian)", true) && p.err == nil
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
	values["addr"] = p.ask("addr", "SSH listen address for Aether, not the system sshd", def("addr"))
	values["data-dir"] = p.ask("data-dir", "Data directory", def("data-dir"))

	// Each config a yes writes has the edge off, a web-port or auto-join on,
	// except one requiring a key with the edge on, which is what a no writes.
	_, webPort := current["web-port"]
	autoJoin, _ := strconv.ParseBool(current["tailnet-auto-join"])
	wasTailnet := len(current) > 0 && (current["edge-url"] == "" || webPort || autoJoin)
	tailnet, host := false, ""
	if tailscaled != nil {
		tailnet, host = askTailnet(p, tailscaled, len(current) == 0 || wasTailnet)
	}
	switch {
	case tailnet:
		saved := func(key string) bool {
			on, _ := strconv.ParseBool(def(key))
			return on
		}
		values["tailnet-auto-join"] = strconv.FormatBool(p.confirm(
			"\nLet anyone on the tailnet join without an admin approving them", saved("tailnet-auto-join")))
		// A keyed tailnet server's config can match what a no writes, so a
		// saved key requirement is offered back: Enter never drops it.
		required := p.confirm("Also require a registered SSH key, for tailnet devices more than one person uses",
			saved("tailnet-require-key"))
		values["tailnet-require-key"] = strconv.FormatBool(required)

		// The dashboard cannot present a key, so it is only offered where it
		// could start: tailscaled on this host and no key requirement. Only a
		// first run probes for the default: on a live server the dashboard
		// itself holds the port.
		if !required {
			if host == "" {
				host = "<this host's MagicDNS name>"
			}
			_, _ = fmt.Fprintf(w, "\nThe server can serve the dashboard at https://%s/ to phones and\nbrowsers on the tailnet, which needs MagicDNS and HTTPS certificates on.\n", host)
			webDefault := def("web-port")
			if _, ok := current["web-port"]; !ok {
				webDefault = strconv.Itoa(dashboardPort)
				if err := dashboardBlocked(tailscaled, dashboardPort); err != nil {
					webDefault = "0"
					_, _ = fmt.Fprintf(w, "Defaulting to 0: %v, then run: sudo aether-server config set web-port %d\n", err, dashboardPort)
				}
			}
			values["web-port"] = p.ask("web-port", "Dashboard HTTPS port (0 = off)", webDefault)
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
// tailscaled is on. Enter says yes when tailscaled names the tailnet and
// def is set, and no otherwise. It also returns this host's MagicDNS name,
// or "" when tailscaled cannot say.
func askTailnet(p *prompter, tailscaled *reachability.Tailscale, def bool) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	node, err := tailscaled.Self(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(p.w, "\ntailscaled is installed, but setup cannot read which tailnet it is on: %v\n", err)
		return p.confirm("Use Tailscale to reach this server", false), ""
	}
	_, _ = fmt.Fprintf(p.w, "\nThis host is on the tailnet %s.\n", node.Tailnet)
	_, _ = fmt.Fprintln(p.w, "With Tailscale, people on it link by this host's name and sign in with their")
	_, _ = fmt.Fprintln(p.w, "tailnet login: no keys, invite codes or relay. Answer no to use Aether's relay")
	_, _ = fmt.Fprintln(p.w, "instead, such as when your computer is not on this tailnet.")
	return p.confirm("Use Tailscale to reach this server", def), node.DNSName
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
// already has it on, and the disclosure follows a yes. An edge-url the operator already set is kept or
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
	if tailnet {
		values["edge-url"] = ""
		_, _ = fmt.Fprintf(p.w, "\nDevices off the tailnet can reach this server through the edge, Aether's relay\nat %s%s.\n", edgeURL, operator)
		if !p.confirm("Turn on the edge too", configured != "") {
			return
		}
		values["edge-url"] = edgeURL
		printEdgeDisclosure(p.w)
		return
	}
	switch {
	case tailscaled:
		_, _ = fmt.Fprintf(p.w, "\nSetup turns on the edge, Aether's relay at %s%s.\n", edgeURL, operator)
	default:
		_, _ = fmt.Fprintf(p.w, "\nTailscale is not installed, so setup turns on the edge, Aether's relay at\n%s%s. To use Tailscale\ninstead, install it (https://tailscale.com/download) and run setup again.\n", edgeURL, operator)
	}
	printEdgeDisclosure(p.w)
	values["edge-url"] = edgeURL
}

func printEdgeDisclosure(w io.Writer) {
	_, _ = fmt.Fprintln(w, "This server and your computer both dial out to the edge, so neither needs an")
	_, _ = fmt.Fprintln(w, "open port. SSH runs end to end through it, so the edge cannot read it. It sees")
	_, _ = fmt.Fprintln(w, "this server's host name, host key and IP address, who signs in, device names,")
	_, _ = fmt.Fprintln(w, "client IP addresses, and when and how much traffic flows.")
	_, _ = fmt.Fprintln(w, `Turn it off with: aether-server config set edge-url ""`)
}

const edgeAccessQuestion = `
Who may reach this server through the edge?
  1) Account access: anyone you invite, once they sign in with GitHub.
  2) Approved devices: each new device also waits until that person, an admin
     or you approve it, so a taken-over GitHub account or edge cannot add one.
     Recommended for a team with code or credentials to protect.
Tailscale and SSH key access stay as they are. Change it later with:
  aether-server config set edge-access <account|approved-devices>
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
// takes, and asks again until the answer is yes, no or a boolean.
func (p *prompter) confirm(label string, def bool) bool {
	word := "no"
	if def {
		word = "yes"
	}
	for {
		answer := strings.ToLower(p.readLine(label+" (yes/no)", word))
		switch answer {
		case "y", "yes":
			return true
		case "n", "no":
			return false
		}
		if b, err := strconv.ParseBool(answer); err == nil {
			return b
		}
		if p.err != nil || p.eof {
			return def
		}
		_, _ = fmt.Fprintln(p.w, "  answer yes or no; try again")
	}
}

// errInputEnded stops setup when input ends at a prompt, so Ctrl-D never
// writes files or installs Sysbox on defaults nobody accepted.
var errInputEnded = errors.New("setup: input ended before every question was answered; nothing written")

func (p *prompter) readLine(label, def string) string {
	if p.eof && p.err == nil {
		p.err = errInputEnded
	}
	if p.err != nil {
		return def
	}
	_, _ = fmt.Fprintf(p.w, "%s [%s]: ", label, def)
	line, err := p.lines.ReadString('\n')
	switch {
	case err == io.EOF:
		p.eof = true
		if strings.TrimSpace(line) == "" {
			p.err = errInputEnded
		}
	case err != nil:
		p.err = err
		return def
	}
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	return def
}
