package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
)

// ErrGitHubNotLoggedIn reports that gh in the member's environment
// terminal is not logged in to github.com yet.
var ErrGitHubNotLoggedIn = errors.New("github: not logged in to github.com in the environment terminal; run gh auth login there first")

// ghAuthStatus is the shape of gh auth status --json hosts.
type ghAuthStatus struct {
	Hosts map[string][]struct {
		State  string `json:"state"`
		Active bool   `json:"active"`
		Login  string `json:"login"`
	} `json:"hosts"`
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
	// container out from under the three execs.
	lock := s.terminalLock(member)
	lock.Lock()
	defer lock.Unlock()

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
	login, ok := activeGitHubLogin(code, stdout)
	if !ok {
		return domain.GitHubConnection{}, fmt.Errorf("%w: %s", ErrGitHubNotLoggedIn, said)
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
	return domain.GitHubConnection{Login: login, SigningKey: pub, Fingerprint: fingerprint}, nil
}

// activeGitHubLogin reads the account gh reports as the active, logged-in
// one for github.com. Anything else - a nonzero exit, unparsable output,
// no successful active entry - is "not logged in".
func activeGitHubLogin(code int, stdout string) (string, bool) {
	if code != 0 {
		return "", false
	}
	var status ghAuthStatus
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		return "", false
	}
	for _, host := range status.Hosts["github.com"] {
		if host.Active && host.State == "success" && host.Login != "" {
			return host.Login, true
		}
	}
	return "", false
}

func signingFingerprint(publicKeyLine string) (string, error) {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKeyLine))
	if err != nil {
		return "", fmt.Errorf("scheduler: parse signing public key: %w", err)
	}
	return ssh.FingerprintSHA256(key), nil
}
