package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
)

// ErrGitHubNotLoggedIn reports that gh in the member's environment
// terminal is not logged in to github.com yet.
var ErrGitHubNotLoggedIn = errors.New("github: not logged in to github.com in the environment terminal; run gh auth login there first")

// ErrGitHubScopeMissing reports that the login is good but was granted
// without the scope the last step of the connect needs.
var ErrGitHubScopeMissing = errors.New("github: the gh login on github.com lacks the admin:ssh_signing_key scope; run gh auth refresh -h github.com -s admin:ssh_signing_key in the environment terminal")

// signingKeyScope is the gh scope that lets ssh-key add register a signing
// key on the member's account.
const signingKeyScope = "admin:ssh_signing_key"

// githubConnectTimeout bounds the whole connect, which holds the member's
// terminal lock while it runs commands that reach github.com. A gh the
// member's container has shadowed could otherwise block every other call
// for that member for as long as the SSH channel stays open. Tests shorten
// it.
var githubConnectTimeout = 2 * time.Minute

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
		return code, stdout, strings.TrimSpace(stdout + stderr), err
	}

	code, stdout, said, err := gh("gh", "auth", "status", "--hostname", "github.com", "--active", "--json", "hosts")
	if err != nil {
		return domain.GitHubConnection{}, fmt.Errorf("scheduler: check github login: %w", err)
	}
	if code == 126 || code == 127 {
		return domain.GitHubConnection{}, fmt.Errorf("scheduler: gh is not on PATH in the environment terminal (the standard image ships it; a saved environment may need it installed): %s", said)
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

// activeGitHubLogin returns the account gh reports as the active one for
// github.com and whether it is logged in. A failed entry comes back too,
// for the error it carries; unparsable output comes back as no entry.
func activeGitHubLogin(stdout string) (ghAuthEntry, bool) {
	var status ghAuthStatus
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		return ghAuthEntry{}, false
	}
	for _, host := range status.Hosts["github.com"] {
		if host.Active {
			return host, host.State == "success" && host.Login != ""
		}
	}
	return ghAuthEntry{}, false
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
