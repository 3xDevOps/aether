package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/shellquote"
)

// ErrGitHubNotLoggedIn reports that gh in the member's environment
// terminal is not logged in to github.com yet.
var ErrGitHubNotLoggedIn = errors.New("github: not logged in to github.com in the environment terminal; run gh auth login there first")

// ErrGitHubScopeMissing reports that the login is good but was granted
// without the scope the last step of the connect needs.
var ErrGitHubScopeMissing = errors.New("github: the gh login on github.com lacks the admin:ssh_signing_key scope; run gh auth refresh -h github.com -s admin:ssh_signing_key in the environment terminal")

// ErrGitHubCLIMissing reports that the member's environment terminal has no
// gh at all, which is where every environment saved, or standard image
// pulled, before the standard image started shipping gh ends up.
var ErrGitHubCLIMissing = errors.New("github: gh is not on PATH in the environment terminal")

// ErrGitHubCLIBroken reports a gh that is on PATH but would not run.
var ErrGitHubCLIBroken = errors.New("github: gh in the environment terminal would not run")

// ErrGitHubCLIOutdated reports a gh too old to answer the login check.
var ErrGitHubCLIOutdated = errors.New("github: the gh in the environment terminal is too old to check the login")

// signingKeyScope is the gh scope that lets ssh-key add register a signing
// key on the member's account.
const signingKeyScope = "admin:ssh_signing_key"

// minGitHubCLI is the oldest gh whose auth status answers --json, which is
// how the login check reads gh's own view of the account. gh 2.81.0 added
// the flag (cli/cli#11544); every release before it rejects the argv and
// exits 1, and that used to be reported to the member as a failed login.
var minGitHubCLI = []int{2, 81, 0}

// releaseTagPattern matches a Docker tag that names one Aether release: the
// release workflow's accepted refs that are also valid tags, with a
// prerelease part that may contain hyphens (v1.2.3-rc-1). A registry never
// moves such a tag, so repulling it cannot change what the server runs.
var releaseTagPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)

// ReleaseTag reports a tag that names one Aether release. The server builds
// its default standard image from it; the remedy for an image on such a tag
// is a newer server, not a repull.
func ReleaseTag(tag string) bool { return releaseTagPattern.MatchString(tag) }

// ghVersionPattern matches the release triple on the first line of
// gh --version, "gh version 2.100.0 (2026-09-03)". Distribution packages
// append their own build to that line, so only the leading triple is read.
var ghVersionPattern = regexp.MustCompile(`gh version (\d+)\.(\d+)\.(\d+)`)

// githubConnectTimeout bounds the whole connect, which holds the member's
// terminal lock while it runs commands that reach github.com. A gh the
// member's container has shadowed could otherwise block every other call
// for that member for as long as the SSH channel stays open. Tests shorten
// it.
var githubConnectTimeout = 2 * time.Minute

// githubProbeTimeout bounds the probe, which is one gh --version in a
// container that is already running. It rides the member's own gateway
// connection, which drops a round-trip at sixty seconds and takes their
// terminal with it, so the probe gives up well before that.
var githubProbeTimeout = 20 * time.Second

// ghAuthStatus is the shape of gh auth status --json hosts.
type ghAuthStatus struct {
	Hosts map[string][]ghAuthEntry `json:"hosts"`
}

// ghAuthEntry is one account gh knows for a host.
type ghAuthEntry struct {
	State  string `json:"state"`
	Active bool   `json:"active"`
	Login  string `json:"login"`
	// Scopes is gh's own comma-separated rendering, "gist, read:org, repo".
	Scopes string `json:"scopes"`
	// Error is what gh could not do with this account - an expired or
	// revoked token reads as "HTTP 401: Bad credentials".
	Error string `json:"error"`
}

// ConnectGitHub finishes the GitHub connection the member started with
// gh auth login in their environment terminal: it confirms the login,
// hands git the gh credential helper, generates the member's commit
// signing key in their home, writes the signing settings and their git
// identity into the home's .gitconfig, and registers the public key on
// the account.
//
// gh is only ever run inside the member's own terminal container, which
// is where their login lives; nothing in the flow runs a program named by
// the home.
func (s *Scheduler) ConnectGitHub(ctx context.Context, member domain.MemberID) (domain.GitHubConnection, error) {
	// Held for the whole call so a concurrent stop cannot pull the
	// container out from under the four execs.
	lock := s.terminalLock(member)
	lock.Lock()
	defer lock.Unlock()

	ctx, cancel := context.WithTimeout(ctx, githubConnectTimeout)
	defer cancel()

	sup := s.lookupTerminal(member)
	if sup == nil {
		return domain.GitHubConnection{}, ErrTerminalNotRunning
	}
	m, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return domain.GitHubConnection{}, fmt.Errorf("scheduler: get member to connect github: %w", err)
	}
	plan, err := s.BuildEnvironmentPlan(ctx, nil, nil, m, harness.Profile{}, EnvironmentPurposeTerminal)
	if err != nil {
		return domain.GitHubConnection{}, fmt.Errorf("scheduler: build github environment: %w", err)
	}
	// stdout is what a command answers; stderr joins it only in the error
	// a member reads, so a warning gh prints beside its JSON cannot break
	// the parse.
	gh := func(argv ...string) (code int, stdout, said string, err error) {
		code, stdout, stderr, err := s.cfg.Runtime.Exec(ctx, sup.containerID, argv, plan.Home)
		return code, stdout, joinOutput(stdout, stderr), err
	}

	cli, err := s.probeGitHubCLI(ctx, sup.containerID, plan.Home)
	if err != nil {
		return domain.GitHubConnection{}, err
	}
	if cli.Status != domain.GitHubCLIOK {
		_, _, phrase := s.githubCLIRemedy(m, sup.image, s.memberOwnedGitHubCLI(ctx, sup.containerID))
		switch cli.Status {
		case domain.GitHubCLIMissing:
			return domain.GitHubConnection{}, fmt.Errorf("%w; %s: %s", ErrGitHubCLIMissing, phrase, cli.Detail)
		case domain.GitHubCLIBroken:
			return domain.GitHubConnection{}, fmt.Errorf("%w; %s: %s", ErrGitHubCLIBroken, phrase, cli.Detail)
		default:
			// gh's own version line names what is there, so the message
			// carries it rather than paraphrasing it.
			return domain.GitHubConnection{}, fmt.Errorf("%w: %s is the oldest gh that answers auth status --json; %s: %s",
				ErrGitHubCLIOutdated, cli.Minimum, phrase, cli.Detail)
		}
	}

	// No --active: the flag landed in gh 2.57.0 and the active entry is
	// picked out of the JSON here anyway.
	code, stdout, said, err := gh("gh", "auth", "status", "--hostname", "github.com", "--json", "hosts")
	if err != nil {
		return domain.GitHubConnection{}, fmt.Errorf("scheduler: check github login: %w", err)
	}
	if code != 0 {
		// With --json gh exits 0 whatever it thinks of the account - no
		// login at all answers {"hosts":{}} - so a non-zero exit is a
		// fatal error, and the one thing it never means is "log in".
		return domain.GitHubConnection{}, fmt.Errorf("scheduler: gh auth status exited %d: %s", code, said)
	}
	entry, ok := activeGitHubLogin(stdout)
	if !ok {
		// gh's own account error says why far better than its JSON does;
		// without an entry there is nothing to quote but what gh printed.
		detail := entry.Error
		if detail == "" {
			detail = said
		}
		return domain.GitHubConnection{}, fmt.Errorf("%w: %s", ErrGitHubNotLoggedIn, detail)
	}
	// Refused before anything in the home is touched: a member who has to
	// go back to gh auth refresh should not first collect a signing key,
	// a rewritten .gitconfig and a git credential helper.
	if !slices.ContainsFunc(strings.Split(entry.Scopes, ","), func(scope string) bool {
		return strings.TrimSpace(scope) == signingKeyScope
	}) {
		return domain.GitHubConnection{}, ErrGitHubScopeMissing
	}
	// Signing happens on the server, so the server host is what needs the
	// signing program; failing here beats writing a key nothing can use.
	if _, lookErr := exec.LookPath("ssh-keygen"); lookErr != nil {
		return domain.GitHubConnection{}, fmt.Errorf("scheduler: the server host needs ssh-keygen (openssh-client) to sign commits: %w", lookErr)
	}
	if s.cfg.Homes == nil {
		return domain.GitHubConnection{}, errors.New("scheduler: member homes are not configured; github.connect needs one to keep the signing key")
	}

	code, _, said, err = gh("gh", "auth", "setup-git", "--hostname", "github.com")
	if err != nil {
		return domain.GitHubConnection{}, fmt.Errorf("scheduler: gh auth setup-git: %w", err)
	}
	if code != 0 {
		return domain.GitHubConnection{}, fmt.Errorf("scheduler: gh auth setup-git exited %d: %s", code, said)
	}

	pub, err := s.cfg.Homes.EnsureSigningKey(member)
	if err != nil {
		return domain.GitHubConnection{}, fmt.Errorf("scheduler: prepare commit signing key: %w", err)
	}
	if cfgErr := s.cfg.Homes.ConfigureGit(ctx, member, m.GitIdentity()); cfgErr != nil {
		return domain.GitHubConnection{}, fmt.Errorf("scheduler: configure git in the member home: %w", cfgErr)
	}

	title := "aether " + string(member)
	// gh ssh-key add takes no --hostname: the key goes to gh's default
	// host, which is github.com unless the home sets GH_HOST.
	code, _, said, err = gh("gh", "ssh-key", "add", ".ssh/aether_signing.pub", "--type", "signing", "--title", title)
	if err != nil {
		return domain.GitHubConnection{}, fmt.Errorf("scheduler: register signing key on github: %w", err)
	}
	if code != 0 {
		return domain.GitHubConnection{}, fmt.Errorf("scheduler: gh ssh-key add exited %d: %s", code, said)
	}

	fingerprint, err := signingFingerprint(pub)
	if err != nil {
		return domain.GitHubConnection{}, err
	}
	// The upload names a path in the home, which the container writes; the
	// fingerprint reported here comes from the private key. Reading the
	// account back is what makes those the same key.
	code, listed, said, err := gh("gh", "ssh-key", "list")
	if err != nil {
		return domain.GitHubConnection{}, fmt.Errorf("scheduler: list github signing keys: %w", err)
	}
	if code != 0 {
		return domain.GitHubConnection{}, fmt.Errorf("scheduler: gh ssh-key list exited %d: %s", code, said)
	}
	if !listsFingerprint(listed, fingerprint) {
		return domain.GitHubConnection{}, fmt.Errorf(
			"github: the signing key registered on the account does not match this member's key (fingerprint %s not listed)", fingerprint)
	}
	return domain.GitHubConnection{Login: entry.Login, SigningKey: pub, Fingerprint: fingerprint}, nil
}

// ProbeGitHubCLI reports the gh in the member's environment terminal before
// the member is told to run gh auth login in it. An environment saved, or a
// standard image pulled, before the image started shipping gh has none, and
// a gh too old for the login check would fail that login for a reason that
// has nothing to do with GitHub.
//
// It answers only for a terminal that is already running, and it neither
// opens a container nor resolves an image: both can pull, and the gateway
// carrying this call drops a control round-trip that takes more than sixty
// seconds, along with the connection the member's own terminal rides on.
// It takes no terminal lock for the same reason - the connect holds that
// for two minutes - and one gh --version mutates nothing; a container that
// goes away under it comes back as a terminal that is not running.
func (s *Scheduler) ProbeGitHubCLI(ctx context.Context, member domain.MemberID) (domain.GitHubCLI, error) {
	ctx, cancel := context.WithTimeout(ctx, githubProbeTimeout)
	defer cancel()

	sup := s.lookupTerminal(member)
	if sup == nil {
		return domain.GitHubCLI{}, ErrTerminalNotRunning
	}
	m, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return domain.GitHubCLI{}, fmt.Errorf("scheduler: get member to probe gh: %w", err)
	}
	// No working directory: gh --version needs none, and asking for the
	// home would mean resolving the image's user, which pulls.
	cli, err := s.probeGitHubCLI(ctx, sup.containerID, "")
	// A container stopped under the exec answers an exit code, not an
	// error, and a container replaced under it answers for one the member
	// no longer has. Every answer describes the terminal it was asked of,
	// so it is only worth returning while that is still the terminal.
	if s.lookupTerminal(member) != sup {
		return domain.GitHubCLI{}, ErrTerminalNotRunning
	}
	if err != nil {
		return domain.GitHubCLI{}, err
	}
	cli.Image = sup.image
	cli.SavedImage = m.Image
	if cli.Status != domain.GitHubCLIOK {
		cli.Path = s.memberOwnedGitHubCLI(ctx, sup.containerID)
		cli.Remedy, cli.AdminRemedy, _ = s.githubCLIRemedy(m, sup.image, cli.Path)
	}
	return cli, nil
}

// activeGitHubLogin returns the account gh reports as the active one for
// github.com and whether it is logged in. A failed entry comes back too,
// for the error it carries; unparsable output comes back as no entry.
func activeGitHubLogin(stdout string) (ghAuthEntry, bool) {
	var status ghAuthStatus
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		return ghAuthEntry{}, false
	}
	hosts := status.Hosts["github.com"]
	for _, host := range hosts {
		if host.Active {
			return host, host.State == "success" && host.Login != ""
		}
	}
	// One account and no active marker is still that account. Every gh
	// this check accepts writes the field, so this is for a gh - or a
	// stand-in for one - that answers the JSON without it.
	if len(hosts) == 1 {
		return hosts[0], hosts[0].State == "success" && hosts[0].Login != ""
	}
	return ghAuthEntry{}, false
}

// probeGitHubCLI asks the member's own container what gh it has. A gh that
// is not there comes back as the exit code the container runtime reports
// for a missing executable, so the answer is the same exec path the rest of
// the connect uses rather than a guess about the image.
func (s *Scheduler) probeGitHubCLI(ctx context.Context, container runtime.ID, home string) (domain.GitHubCLI, error) {
	code, stdout, stderr, err := s.cfg.Runtime.Exec(ctx, container, []string{"gh", "--version"}, home)
	if err != nil {
		return domain.GitHubCLI{}, fmt.Errorf("scheduler: probe gh in the environment terminal: %w", err)
	}
	cli := domain.GitHubCLI{Detail: joinOutput(stdout, stderr), Minimum: versionString(minGitHubCLI)}
	switch {
	case code == 127:
		// 127 is the container runtime's answer for an executable that is
		// not there; 126 is one that is, and would not run.
		cli.Status = domain.GitHubCLIMissing
		return cli, nil
	case code != 0:
		cli.Status = domain.GitHubCLIBroken
		cli.Detail = fmt.Sprintf("gh --version exited %d", code)
		if said := joinOutput(stdout, stderr); said != "" {
			cli.Detail += ": " + said
		}
		return cli, nil
	}
	cli.Status = domain.GitHubCLIOK
	found, ok := parseGitHubCLIVersion(stdout)
	if !ok {
		// A gh whose version line cannot be read - a source build, a
		// wrapper script - is not evidence of an old gh. Its own answer
		// to auth status judges it better than this regexp can.
		return cli, nil
	}
	cli.Version = versionString(found)
	if slices.Compare(found, minGitHubCLI) < 0 {
		cli.Status = domain.GitHubCLIOutdated
	}
	return cli, nil
}

// memberOwnedGitHubCLI reports the gh the container resolves when it is a
// file in the member's own environment home, and "" otherwise. That home is
// bind-mounted with its .local/bin first on PATH, so a gh a member installed
// there shadows the image's and outlives every image remedy - which makes it
// the only answer worth printing when it is the one that answered.
//
// It runs only for a gh that cannot do the login, in a container that is
// already up, so it costs one exec and never a pull. A container without a
// shell, or one that answers something else, reports nothing rather than
// guessing.
func (s *Scheduler) memberOwnedGitHubCLI(ctx context.Context, container runtime.ID) string {
	const askPath = `printf '%s\n%s\n' "$HOME" "$(command -v gh || true)"`
	code, stdout, _, err := s.cfg.Runtime.Exec(ctx, container, []string{"sh", "-c", askPath}, "")
	if err != nil || code != 0 {
		return ""
	}
	home, path, ok := strings.Cut(strings.TrimSpace(stdout), "\n")
	if !ok {
		return ""
	}
	home, path = strings.TrimSpace(home), strings.TrimSpace(path)
	if home == "" || path == "" || !strings.HasPrefix(path, home+"/") {
		return ""
	}
	return path
}

// githubCLIRemedy is what has to happen for this member's environment
// terminal to get a gh that can do the login: the command the member runs,
// the command a server admin has to run first when the server's own
// standard image is the one without it, and the sentence a refusal carries.
//
// Stopping the terminal is half of every answer that is not a reset. A
// container keeps the image it started from, and nothing recreates it while
// it runs, so a newer image on the server reaches the member only when they
// open the terminal again.
func (s *Scheduler) githubCLIRemedy(m *domain.Member, running, path string) (member, admin, phrase string) {
	switch {
	case path != "":
		// Their own file, first on PATH, kept across every image: no
		// image the server can give them gets past it. The path is a
		// name they chose, and this is printed to be pasted.
		remove := "rm " + shellquote.Quote(path)
		return remove, "",
			fmt.Sprintf("the gh that answered is %s, a file in your own environment home, so it survives every image: %s to let the image's own gh take over, or replace it with %s or newer",
				path, remove, versionString(minGitHubCLI))
	case m.Image != "":
		// A saved image is a commit of this member's own container, so a
		// newer standard image cannot reach it and reopening the terminal
		// gives them the same filesystem back.
		return "aether env reset", "",
			"install a current gh there and save the environment again, or run aether env reset to remove the saved image and return to the standard one"
	case running != s.cfg.StandardImage:
		return "aether terminal stop", "",
			"run aether terminal stop and open the terminal again: it is still running the image it started from"
	default:
		admin = s.standardImageRemedy()
		return "aether terminal stop", admin,
			fmt.Sprintf("ask a server admin to run %s, then run aether terminal stop and open the terminal again", admin)
	}
}

// standardImageRemedy is what gets the server a newer standard image.
//
// A server update only moves the image when the server is taking this
// build's default: an operator who pinned --standard-image keeps that value
// across the update, and the pin is what has to move. A tag naming one
// release never moves in a registry either, so repulling it changes
// nothing; every other reference is repulled where the server can see it.
func (s *Scheduler) standardImageRemedy() string {
	ref := s.cfg.StandardImage
	switch {
	case ref == s.cfg.DefaultStandardImage && releaseTagged(ref):
		return "aether server update"
	case releaseTagged(ref) || strings.Contains(ref, "@"):
		// config set rewrites the options file; the running server keeps
		// the old flag until it is restarted.
		return "sudo aether-server config set standard-image <a newer image> && sudo systemctl restart aether-server"
	default:
		return "docker pull " + ref
	}
}

// releaseTagged reports a reference whose tag names one release.
func releaseTagged(ref string) bool {
	tag := strings.LastIndex(ref, ":")
	return tag >= 0 && releaseTagPattern.MatchString(ref[tag+1:])
}

// joinOutput is what a command said, both streams, without running the last
// line of one into the first of the other.
func joinOutput(stdout, stderr string) string {
	parts := make([]string, 0, 2)
	for _, out := range []string{stdout, stderr} {
		if trimmed := strings.TrimSpace(out); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "\n")
}

func parseGitHubCLIVersion(out string) ([]int, bool) {
	match := ghVersionPattern.FindStringSubmatch(out)
	if match == nil {
		return nil, false
	}
	parts := make([]int, 0, 3)
	for _, group := range match[1:] {
		n, err := strconv.Atoi(group)
		if err != nil {
			return nil, false
		}
		parts = append(parts, n)
	}
	return parts, true
}

func versionString(parts []int) string {
	out := make([]string, len(parts))
	for i, n := range parts {
		out[i] = strconv.Itoa(n)
	}
	return strings.Join(out, ".")
}

// listsFingerprint reports whether gh ssh-key list names fingerprint. Each
// line is tab separated: title, fingerprint, added, id, type.
func listsFingerprint(out, fingerprint string) bool {
	for line := range strings.Lines(out) {
		if slices.Contains(strings.Split(strings.TrimSpace(line), "\t"), fingerprint) {
			return true
		}
	}
	return false
}

func signingFingerprint(publicKeyLine string) (string, error) {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKeyLine))
	if err != nil {
		return "", fmt.Errorf("scheduler: parse signing public key: %w", err)
	}
	return ssh.FingerprintSHA256(key), nil
}
