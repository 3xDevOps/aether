// Package github reads native gh credentials only for fixed, read-only GitHub API requests.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"gopkg.in/yaml.v3"
)

const (
	credentialPath  = ".config/gh/hosts.yml"
	credentialLimit = 128 * 1024
	responseLimit   = 8 * 1024 * 1024
	requestTimeout  = 15 * time.Second
)

type Account struct {
	ID    int64
	Login string
}

type Repository struct {
	ID                      int64
	FullName, Name          string
	Private                 bool
	DefaultBranch, CloneURL string
	CanPush                 bool
}

type RepositoryPage struct {
	Account      Account
	Repositories []Repository
	NextPage     int
}

type Service struct {
	homes  *memberhome.Manager
	client *http.Client
}

func New(homes *memberhome.Manager) *Service {
	return &Service{
		homes: homes,
		client: &http.Client{
			Timeout:       requestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Credentials is server-only: callers must never persist or expose its token.
// Each call verifies the active native account, rather than trusting a cached login.
func (s *Service) Credentials(ctx context.Context, member domain.MemberID) (string, Account, error) {
	if s.homes == nil {
		return "", Account{}, errors.New("github: member homes unavailable")
	}
	data, err := s.homes.ReadCredential(member, credentialPath, credentialLimit)
	if err != nil {
		// Native file errors can contain attacker-controlled symlink destinations.
		return "", Account{}, errors.New("github: cannot read native gh credentials safely")
	}
	token, login, err := parseCredential(data)
	if err != nil {
		return "", Account{}, err
	}
	var account Account
	if _, err := s.get(ctx, token, "/user", &account); err != nil {
		return "", Account{}, err
	}
	if account.ID <= 0 || !ValidLogin(account.Login) {
		return "", Account{}, errors.New("github: API returned an invalid account identity")
	}
	if !strings.EqualFold(login, account.Login) {
		return "", Account{}, errors.New("github: native gh active account differs from the authenticated account; reconnect GitHub")
	}
	return token, account, nil
}

func (s *Service) Repositories(ctx context.Context, member domain.MemberID, page int) (RepositoryPage, error) {
	if page == 0 {
		page = 1
	}
	if page < 1 {
		return RepositoryPage{}, errors.New("github: repository page must be positive")
	}
	token, account, err := s.Credentials(ctx, member)
	if err != nil {
		return RepositoryPage{}, err
	}
	query := url.Values{
		"affiliation": {"owner,collaborator,organization_member"},
		"visibility":  {"all"},
		"sort":        {"full_name"},
		"direction":   {"asc"},
		"per_page":    {"100"},
		"page":        {strconv.Itoa(page)},
	}
	var records []repositoryResponse
	header, err := s.get(ctx, token, "/user/repos?"+query.Encode(), &records)
	if err != nil {
		return RepositoryPage{}, err
	}
	if len(records) > 100 {
		return RepositoryPage{}, errors.New("github: API returned too many repositories in one page")
	}
	next, err := nextPage(header.Values("Link"), page)
	if err != nil {
		return RepositoryPage{}, err
	}
	result := RepositoryPage{Account: account, Repositories: make([]Repository, 0, len(records)), NextPage: next}
	for _, record := range records {
		repo, err := record.repository()
		if err != nil {
			return RepositoryPage{}, err
		}
		result.Repositories = append(result.Repositories, repo)
	}
	return result, nil
}

func (s *Service) Repository(ctx context.Context, member domain.MemberID, fullName string) (Account, Repository, error) {
	if !validFullName(fullName) {
		return Account{}, Repository{}, errors.New("github: repository must be an owner/name on github.com")
	}
	token, account, err := s.Credentials(ctx, member)
	if err != nil {
		return Account{}, Repository{}, err
	}
	var record repositoryResponse
	if _, err = s.get(ctx, token, "/repos/"+fullName, &record); err != nil {
		return Account{}, Repository{}, err
	}
	repo, err := record.repository()
	if err != nil {
		return Account{}, Repository{}, err
	}
	if !strings.EqualFold(repo.FullName, fullName) {
		return Account{}, Repository{}, errors.New("github: API returned a different repository")
	}
	return account, repo, nil
}

func parseCredential(data []byte) (string, string, error) {
	if len(data) == 0 {
		return "", "", errors.New("github: no native gh login found; connect GitHub")
	}
	type user struct {
		Token string `yaml:"oauth_token"`
	}
	type host struct {
		Token string          `yaml:"oauth_token"`
		User  string          `yaml:"user"`
		Users map[string]user `yaml:"users"`
	}
	var hosts map[string]host
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&hosts); err != nil {
		return "", "", errors.New("github: malformed native gh credentials; reconnect GitHub")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", "", errors.New("github: malformed native gh credentials; reconnect GitHub")
	}
	active, ok := hosts["github.com"]
	if !ok || !ValidLogin(active.User) {
		return "", "", errors.New("github: no active github.com account in native gh credentials; connect GitHub")
	}
	// gh keeps the active token at the host root. Some multi-account stores
	// keep it only under users; never choose an arbitrary saved account.
	token := active.Token
	if token == "" {
		token = active.Users[active.User].Token
	}
	if token == "" || len(token) > 16384 {
		return "", "", errors.New("github: active native gh account has no usable token; reconnect GitHub")
	}
	for _, c := range token {
		if c < 33 || c > 126 {
			return "", "", errors.New("github: invalid native gh token; reconnect GitHub")
		}
	}
	return token, active.User, nil
}

// get never forwards redirects or returns provider-controlled errors. Status
// codes retain the useful native API failure context without reflecting tokens,
// authorization headers, response bodies, or transport error strings.
func (s *Service) get(ctx context.Context, token, path string, dst any) (http.Header, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com"+path, nil)
	if err != nil || req.URL.Scheme != "https" || req.URL.Host != "api.github.com" || req.URL.User != nil {
		return nil, errors.New("github: invalid API request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Aether")
	response, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("github: API request: %w", ctx.Err())
		}
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			return nil, errors.New("github: API request timed out")
		}
		return nil, errors.New("github: API request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: API request: HTTP %d %s", response.StatusCode, http.StatusText(response.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil {
		return nil, errors.New("github: cannot read API response")
	}
	if len(body) > responseLimit {
		return nil, errors.New("github: API response exceeds size limit")
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return nil, errors.New("github: invalid API response")
	}
	return response.Header, nil
}

type repositoryResponse struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	Permissions   struct {
		Push bool `json:"push"`
	} `json:"permissions"`
}

func (r repositoryResponse) repository() (Repository, error) {
	_, name, _ := strings.Cut(r.FullName, "/")
	if r.ID <= 0 || !validFullName(r.FullName) || r.Name != name {
		return Repository{}, errors.New("github: API returned invalid repository metadata")
	}
	return Repository{
		ID: r.ID, FullName: r.FullName, Name: r.Name, Private: r.Private,
		DefaultBranch: r.DefaultBranch, CloneURL: "https://github.com/" + r.FullName + ".git", CanPush: r.Permissions.Push,
	}, nil
}

// ValidLogin accepts github.com identities, including Enterprise Managed Users
// whose usernames append an underscore and a 3–8 character enterprise shortcode.
// It is not repository-owner validation: credential-bearing paths remain stricter.
func ValidLogin(login string) bool {
	if len(login) > 39 {
		return false
	}
	name, shortcode, managed := strings.Cut(login, "_")
	if !managed {
		return validOwner(login)
	}
	if !validOwner(name) || len(shortcode) < 3 || len(shortcode) > 8 {
		return false
	}
	for _, c := range shortcode {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

func validOwner(owner string) bool {
	if len(owner) < 1 || len(owner) > 39 || owner[0] == '-' || owner[len(owner)-1] == '-' {
		return false
	}
	for _, c := range owner {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-':
		default:
			return false
		}
	}
	return true
}

func validFullName(fullName string) bool {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok || !validOwner(owner) || len(name) < 1 || len(name) > 100 || name == "." || name == ".." {
		return false
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

func nextPage(headers []string, current int) (int, error) {
	for _, header := range headers {
		for _, link := range strings.Split(header, ",") {
			parts := strings.Split(strings.TrimSpace(link), ";")
			isNext := false
			for _, parameter := range parts[1:] {
				key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
				if ok && key == "rel" {
					for _, relation := range strings.Fields(strings.Trim(value, "\"")) {
						isNext = isNext || relation == "next"
					}
				}
			}
			if !isNext {
				continue
			}
			location := strings.TrimSpace(parts[0])
			if !strings.HasPrefix(location, "<") || !strings.HasSuffix(location, ">") {
				return 0, errors.New("github: invalid API pagination link")
			}
			u, err := url.Parse(location[1 : len(location)-1])
			if err != nil || u.Scheme != "https" || u.Host != "api.github.com" || u.User != nil || u.Path != "/user/repos" || u.Fragment != "" {
				return 0, errors.New("github: invalid API pagination link")
			}
			page, err := strconv.Atoi(u.Query().Get("page"))
			if err != nil || page <= current || u.Query().Get("per_page") != "100" {
				return 0, errors.New("github: invalid API pagination page")
			}
			return page, nil
		}
	}
	return 0, nil
}
