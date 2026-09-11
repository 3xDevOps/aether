package mirror

import (
	"errors"
	"net/url"
	"path"
	"strings"
	"unicode"

	"github.com/3xDevOps/Aether/internal/domain"
)

// GitHubKnownHosts is the pinned github.com host key used by deploy-key
// mirrors. It is deliberately kept here rather than trusting the machine's
// global known_hosts file.
const GitHubKnownHosts = "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"

// Source is the canonical source used by a mirror. URL is safe to pass to
// git, while Identity is the stable, scheme-free repository identity stored
// in the domain row.
type Source struct {
	URL        string
	Identity   string
	KnownHosts string
	GitHub     bool
	GenericSSH bool
}

// CanonicalizeSource validates an administrator supplied source and returns
// the URL git should fetch and its stable identity. Deploy-key GitHub HTTPS
// URLs are converted to SSH; GitHub SSH URLs are accepted in their canonical
// form. knownHosts is required for generic SSH and ignored for GitHub.
func CanonicalizeSource(raw string, auth domain.MirrorAuth, knownHosts string) (Source, error) {
	if err := validateSourceText(raw); err != nil {
		return Source{}, err
	}
	if !auth.Valid() {
		return Source{}, errors.New("unknown mirror authentication")
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return Source{}, errors.New("invalid mirror source URL")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return Source{}, errors.New("mirror source must not contain credentials, query, or fragment")
	}
	if u.Host == "" || u.Hostname() == "" {
		return Source{}, errors.New("mirror source host is required")
	}
	if strings.ContainsAny(u.Host, "\x00\r\n\t ") {
		return Source{}, errors.New("mirror source host is invalid")
	}

	host := strings.ToLower(u.Hostname())
	switch auth {
	case domain.MirrorAuthPublic:
		if u.Scheme != "https" {
			return Source{}, errors.New("public mirror source must be credential-free HTTPS")
		}
		if u.User != nil {
			return Source{}, errors.New("public mirror source must be credential-free HTTPS")
		}
		if cleanSourcePath(u.Path) == "" {
			return Source{}, errors.New("mirror source repository path is required")
		}
		if u.Port() != "" && u.Port() != "443" {
			return Source{}, errors.New("public mirror source has an invalid HTTPS port")
		}
		return Source{URL: canonicalHTTPURL(u), Identity: sourceIdentity(host, u.Path)}, nil

	case domain.MirrorAuthDeployKey:
		switch u.Scheme {
		case "https":
			if u.User != nil {
				return Source{}, errors.New("deploy-key HTTPS source must not contain credentials")
			}
			if host != "github.com" || (u.Port() != "" && u.Port() != "443") {
				return Source{}, errors.New("deploy-key HTTPS source must be a GitHub repository")
			}
			owner, repo, ok := githubRepository(u.Path)
			if !ok {
				return Source{}, errors.New("deploy-key GitHub source must be /owner/repository")
			}
			return Source{
				URL:        "ssh://git@github.com/" + owner + "/" + repo + ".git",
				Identity:   "github.com/" + owner + "/" + repo,
				KnownHosts: GitHubKnownHosts,
				GitHub:     true,
			}, nil
		case "ssh":
			if err := validateSSHUserinfo(u); err != nil {
				return Source{}, err
			}
			if host == "github.com" {
				if u.User != nil && u.User.Username() != "git" {
					return Source{}, errors.New("GitHub SSH source must use the git username")
				}
				if u.Port() != "" && u.Port() != "22" {
					return Source{}, errors.New("GitHub SSH source has an invalid SSH port")
				}
				owner, repo, ok := githubRepository(u.Path)
				if !ok {
					return Source{}, errors.New("deploy-key GitHub source must be /owner/repository")
				}
				sshURL := u
				if u.User == nil {
					copy := *u
					copy.User = url.User("git")
					sshURL = &copy
				}
				return Source{
					URL:        canonicalSSHURL(sshURL),
					Identity:   "github.com/" + owner + "/" + repo,
					KnownHosts: GitHubKnownHosts,
					GitHub:     true,
				}, nil
			}
			if cleanSourcePath(u.Path) == "" {
				return Source{}, errors.New("generic SSH source repository path is required")
			}
			if strings.TrimSpace(knownHosts) == "" {
				return Source{}, errors.New("generic SSH mirror source requires known_hosts")
			}
			if strings.ContainsRune(knownHosts, '\x00') {
				return Source{}, errors.New("known_hosts contains NUL")
			}
			return Source{URL: canonicalSSHURL(u), Identity: sourceIdentity(host, u.Path), KnownHosts: knownHosts, GenericSSH: true}, nil
		default:
			return Source{}, errors.New("deploy-key source must use GitHub HTTPS or SSH")
		}
	}
	return Source{}, errors.New("unsupported mirror authentication")
}

func validateSSHUserinfo(u *url.URL) error {
	if u.User == nil {
		return nil
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		return errors.New("SSH mirror source must not contain a password")
	}
	username := u.User.Username()
	if username == "" || len(username) > 64 {
		return errors.New("SSH mirror source username is invalid")
	}
	for i, r := range username {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		if i == 0 || (r != '.' && r != '_' && r != '-') {
			return errors.New("SSH mirror source username is invalid")
		}
	}
	return nil
}

func validateSourceText(raw string) error {
	if raw == "" || strings.HasPrefix(raw, "-") || len(raw) > 2048 {
		return errors.New("mirror source is empty or option-shaped")
	}
	for _, r := range raw {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return errors.New("mirror source must be one line without whitespace")
		}
	}
	return nil
}

func canonicalHTTPURL(u *url.URL) string {
	copy := *u
	copy.Scheme = "https"
	copy.Host = strings.ToLower(u.Host)
	copy.Path = cleanSourcePath(u.Path)
	copy.RawPath = ""
	return copy.String()
}

func canonicalSSHURL(u *url.URL) string {
	copy := *u
	copy.Scheme = "ssh"
	copy.Host = strings.ToLower(u.Host)
	copy.Path = cleanSourcePath(u.Path)
	copy.RawPath = ""
	return copy.String()
}

func cleanSourcePath(p string) string {
	p = path.Clean("/" + p)
	if p == "/." || p == "/" {
		return ""
	}
	return p
}

func sourceIdentity(host, p string) string {
	p = strings.TrimPrefix(cleanSourcePath(p), "/")
	p = strings.TrimSuffix(p, "/")
	p = strings.TrimSuffix(p, ".git")
	return host + "/" + p
}

func githubRepository(p string) (string, string, bool) {
	p = strings.TrimPrefix(cleanSourcePath(p), "/")
	p = strings.TrimSuffix(p, "/")
	p = strings.TrimSuffix(p, ".git")
	parts := strings.Split(p, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	for _, part := range parts {
		if part == "." || part == ".." || strings.ContainsAny(part, "\\\x00\r\n\t ") {
			return "", "", false
		}
	}
	return parts[0], parts[1], true
}
