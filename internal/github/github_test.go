package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/memberhome"
)

const testToken = "gho_test_only_native_credential"

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newTestService(t *testing.T, credentials string, handler transportFunc) (*Service, string) {
	t.Helper()
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	home, err := homes.Path("member-1")
	if err != nil {
		t.Fatal(err)
	}
	if credentials != "" {
		if err := os.MkdirAll(filepath.Join(home, ".config", "gh"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, credentialPath), []byte(credentials), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := New(homes)
	s.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Scheme != "https" || r.URL.Host != "api.github.com" || r.URL.User != nil {
			t.Fatalf("unsafe request: %s %s", r.Method, r.URL.Redacted())
		}
		if _, ok := r.Context().Deadline(); !ok {
			t.Fatal("API request has no deadline")
		}
		return handler(r)
	})
	return s, home
}

func nativeCredentials() string {
	return "github.com:\n  user: octocat\n  oauth_token: " + testToken + "\n"
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func accountResponse() *http.Response {
	return jsonResponse(http.StatusOK, `{"id":42,"login":"octocat"}`)
}

func TestCredentialsNativeLayouts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
		want   string
	}{
		{"legacy", nativeCredentials(), testToken},
		{"current multi-account", "github.com:\n  user: octocat\n  oauth_token: " + testToken + "\n  users:\n    other:\n      oauth_token: inactive\n    octocat:\n      oauth_token: " + testToken + "\n", testToken},
		{"nested active only", "github.com:\n  user: octocat\n  users:\n    other:\n      oauth_token: inactive\n    octocat:\n      oauth_token: " + testToken + "\n", testToken},
		{"root is authoritative", "github.com:\n  user: octocat\n  oauth_token: " + testToken + "\n  users:\n    octocat:\n      oauth_token: stale\n", testToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			s, _ := newTestService(t, tc.config, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer "+tc.want {
					t.Fatal("did not validate the active native token with /user")
				}
				return accountResponse(), nil
			})
			token, account, err := s.Credentials(context.Background(), "member-1")
			if err != nil || token != tc.want || account.ID != 42 || account.Login != "octocat" || calls != 1 {
				t.Fatalf("unexpected credentials result: account=%+v calls=%d err=%v", account, calls, err)
			}
		})
	}
}

func TestManagedAccountCredentialsAndOrganizationRepositories(t *testing.T) {
	for _, login := range []string{"mona-cat_octo", "octo_admin", strings.Repeat("a", 30) + "_12345678"} {
		t.Run(login, func(t *testing.T) {
			config := strings.ReplaceAll(nativeCredentials(), "octocat", login)
			userCalls := 0
			s, _ := newTestService(t, config, func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "Bearer "+testToken {
					t.Fatal("missing managed account authentication")
				}
				const repository = `{"id":123,"full_name":"org/private-repo","name":"private-repo","private":true,"default_branch":"main","permissions":{"push":false}}`
				switch r.URL.Path {
				case "/user":
					userCalls++
					return jsonResponse(http.StatusOK, `{"id":42,"login":"`+login+`"}`), nil
				case "/user/repos":
					return jsonResponse(http.StatusOK, "["+repository+"]"), nil
				case "/repos/org/private-repo":
					return jsonResponse(http.StatusOK, repository), nil
				default:
					t.Fatalf("unexpected authenticated path %s", r.URL.Path)
					return nil, nil
				}
			})
			token, account, err := s.Credentials(context.Background(), "member-1")
			if err != nil || token != testToken || account.ID != 42 || account.Login != login {
				t.Fatalf("managed credentials: %+v %v", account, err)
			}
			page, err := s.Repositories(context.Background(), "member-1", 1)
			if err != nil || page.Account != account || len(page.Repositories) != 1 {
				t.Fatalf("managed repository discovery: %+v %v", page, err)
			}
			boundAccount, repo, err := s.Repository(context.Background(), "member-1", "org/private-repo")
			if err != nil || boundAccount != account || repo != page.Repositories[0] || !repo.Private || repo.CanPush || repo.CloneURL != "https://github.com/org/private-repo.git" || userCalls != 3 {
				t.Fatalf("managed repository read: %+v %+v calls=%d %v", boundAccount, repo, userCalls, err)
			}
			for _, body := range []string{
				`{"id":0,"login":"` + login + `"}`,
				`{"id":42,"login":"other_octo"}`,
			} {
				s.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
					return jsonResponse(http.StatusOK, body), nil
				})
				if token, _, err := s.Credentials(context.Background(), "member-1"); err == nil || token != "" {
					t.Fatal("managed account bypassed verified identity")
				}
			}
		})
	}
}

func TestCredentialsRejectMalformedManagedLogins(t *testing.T) {
	for _, login := range []string{"mona_ab", "mona_123456789", "_octo", "mona__octo", "mona-_octo", "mona_octo/other", "mona_octo?token=x", "mona_octo\n", "mona_octo\x00", strings.Repeat("a", 31) + "_12345678"} {
		t.Run(login, func(t *testing.T) {
			encoded, err := json.Marshal(login)
			if err != nil {
				t.Fatal(err)
			}
			for _, native := range []bool{true, false} {
				config := nativeCredentials()
				if native {
					config = "github.com:\n  user: " + string(encoded) + "\n  oauth_token: " + testToken + "\n"
				}
				s, _ := newTestService(t, config, func(*http.Request) (*http.Response, error) {
					if native {
						t.Fatal("malformed native login reached API")
					}
					return jsonResponse(http.StatusOK, `{"id":42,"login":`+string(encoded)+`}`), nil
				})
				if token, account, err := s.Credentials(context.Background(), "member-1"); err == nil || token != "" || account != (Account{}) {
					t.Fatal("malformed login accepted")
				}
			}
		})
	}
}

func TestCredentialsRejectMissingMalformedAndAmbiguous(t *testing.T) {
	for _, tc := range []struct{ name, config string }{
		{"missing", ""},
		{"empty", "\n"},
		{"malformed", "github.com: ["},
		{"duplicate", "github.com:\n  user: octocat\n  user: other\n  oauth_token: " + testToken},
		{"documents", nativeCredentials() + "---\n" + nativeCredentials()},
		{"enterprise only", "enterprise.example:\n  user: octocat\n  oauth_token: " + testToken},
		{"missing active", "github.com:\n  oauth_token: " + testToken + "\n  users:\n    octocat:\n      oauth_token: " + testToken},
		{"multiple inactive", "github.com:\n  users:\n    octocat:\n      oauth_token: " + testToken + "\n    other:\n      oauth_token: inactive"},
		{"active missing token", "github.com:\n  user: octocat\n  users:\n    other:\n      oauth_token: inactive"},
		{"empty token", "github.com:\n  user: octocat\n  oauth_token: ''"},
		{"newline token", "github.com:\n  user: octocat\n  oauth_token: |\n    " + testToken + "\n"},
		{"embedded newline", "github.com:\n  user: octocat\n  oauth_token: \"before\\nafter\""},
		{"whitespace token", "github.com:\n  user: octocat\n  oauth_token: ' " + testToken + "'"},
		{"oversized file", nativeCredentials() + "#" + strings.Repeat("x", credentialLimit)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestService(t, tc.config, func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid native credentials made an API request")
				return nil, nil
			})
			token, account, err := s.Credentials(context.Background(), "member-1")
			if err == nil || token != "" || account != (Account{}) {
				t.Fatal("invalid credentials were accepted")
			}
			if strings.Contains(err.Error(), testToken) {
				t.Fatal("credential leaked in error")
			}
		})
	}
}

func TestCredentialsRejectSymlink(t *testing.T) {
	s, home := newTestService(t, nativeCredentials(), func(*http.Request) (*http.Response, error) {
		t.Fatal("symlink credentials reached API")
		return nil, nil
	})
	outside := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(outside, []byte(nativeCredentials()), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, credentialPath)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if token, _, err := s.Credentials(context.Background(), "member-1"); err == nil || token != "" {
		t.Fatal("symlink accepted")
	}
}

func TestCredentialsVerifiesIdentityOnEveryCall(t *testing.T) {
	calls := 0
	s, _ := newTestService(t, nativeCredentials(), func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return accountResponse(), nil
		}
		return jsonResponse(http.StatusOK, `{"id":99,"login":"different"}`), nil
	})
	if _, _, err := s.Credentials(context.Background(), "member-1"); err != nil {
		t.Fatal(err)
	}
	if token, account, err := s.Credentials(context.Background(), "member-1"); err == nil || token != "" || account.ID != 0 {
		t.Fatal("changed native account accepted")
	}
	for _, body := range []string{`{"id":0,"login":"octocat"}`, `{"id":-1,"login":"octocat"}`, `{"id":"42","login":"octocat"}`, `{"id":42}`} {
		s.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) { return jsonResponse(http.StatusOK, body), nil })
		if token, _, err := s.Credentials(context.Background(), "member-1"); err == nil || token != "" {
			t.Fatal("invalid numeric identity accepted")
		}
	}
}

func TestRepositoriesIncludesPrivateCollaboratorsAndRealPagination(t *testing.T) {
	s, _ := newTestService(t, nativeCredentials(), func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Fatal("missing authentication")
		}
		if r.URL.Path == "/user" {
			return accountResponse(), nil
		}
		query := r.URL.Query()
		if r.URL.Path != "/user/repos" || query.Get("per_page") != "100" || query.Get("affiliation") != "owner,collaborator,organization_member" || query.Get("visibility") != "all" {
			t.Fatalf("repositories restricted incorrectly: %s", r.URL)
		}
		if query.Get("page") == "1" {
			response := jsonResponse(http.StatusOK, `[{"id":100,"full_name":"other/private-repo","name":"private-repo","private":true,"default_branch":"trunk","permissions":{"push":false,"admin":false},"clone_url":"https://untrusted.invalid/repo.git"}]`)
			response.Header.Set("Link", `<https://api.github.com/user/repos?per_page=100&page=2>; rel="next", <https://api.github.com/user/repos?per_page=100&page=3>; rel="last"`)
			return response, nil
		}
		if query.Get("page") != "2" {
			t.Fatal("wrong next page requested")
		}
		return jsonResponse(http.StatusOK, `[]`), nil
	})
	page, err := s.Repositories(context.Background(), "member-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Account.ID != 42 || page.NextPage != 2 || len(page.Repositories) != 1 {
		t.Fatalf("unexpected page: %+v", page)
	}
	repo := page.Repositories[0]
	if !repo.Private || repo.CanPush || repo.FullName != "other/private-repo" || repo.CloneURL != "https://github.com/other/private-repo.git" || repo.DefaultBranch != "trunk" {
		t.Fatalf("unexpected repository: %+v", repo)
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), testToken) || strings.Contains(string(encoded), "oauth_token") {
		t.Fatal("token in result DTO")
	}
	last, err := s.Repositories(context.Background(), "member-1", page.NextPage)
	if err != nil || last.NextPage != 0 || last.Repositories == nil || len(last.Repositories) != 0 {
		t.Fatalf("empty final page: %+v %v", last, err)
	}
}

func TestRepositoryMetadataAndEmptyDefaultBranch(t *testing.T) {
	for _, branch := range []string{"main", ""} {
		s, _ := newTestService(t, nativeCredentials(), func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/user" {
				return accountResponse(), nil
			}
			if r.URL.Path != "/repos/org/repo" {
				t.Fatalf("unexpected path %s", r.URL.Path)
			}
			return jsonResponse(http.StatusOK, `{"id":123,"full_name":"org/repo","name":"repo","private":true,"default_branch":"`+branch+`","permissions":{"push":true}}`), nil
		})
		account, repo, err := s.Repository(context.Background(), "member-1", "org/repo")
		if err != nil || account.ID != 42 || repo.ID != 123 || repo.DefaultBranch != branch || !repo.CanPush {
			t.Fatalf("metadata: %+v %+v %v", account, repo, err)
		}
	}
}

func TestRejectUnsafeRepositoryInput(t *testing.T) {
	s, _ := newTestService(t, nativeCredentials(), func(*http.Request) (*http.Response, error) { t.Fatal("invalid name reached API"); return nil, nil })
	for _, name := range []string{"https://github.com/org/repo", "org/repo?token=x", "org/repo#fragment", "org/../repo", "../repo", "org/..", "org/repo/extra", "org/%2frepo", "org/repo\n", "mona-cat_octo/repo"} {
		if _, _, err := s.Repository(context.Background(), "member-1", name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	if _, err := s.Repositories(context.Background(), "member-1", -1); err == nil {
		t.Fatal("negative page accepted")
	}
}

func TestRedirectsAreNeverFollowed(t *testing.T) {
	for _, target := range []string{"https://api.github.com/user/redirected", "https://attacker.example/token", "http://api.github.com/user"} {
		calls := 0
		s, _ := newTestService(t, nativeCredentials(), func(*http.Request) (*http.Response, error) {
			calls++
			response := jsonResponse(http.StatusFound, testToken)
			response.Header.Set("Location", target)
			return response, nil
		})
		token, _, err := s.Credentials(context.Background(), "member-1")
		if err == nil || !strings.Contains(err.Error(), "302") || calls != 1 || token != "" {
			t.Fatalf("redirect handling: calls=%d err=%v", calls, err)
		}
		if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), target) {
			t.Fatal("redirect leaked data")
		}
	}
}

func TestAPIErrorsAreSecretSafeAndBounded(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *http.Response
		err      error
		want     string
	}{
		{"unauthorized", jsonResponse(401, `{"message":"`+testToken+`"}`), nil, "401 Unauthorized"},
		{"forbidden", jsonResponse(403, `{"message":"`+testToken+`"}`), nil, "403 Forbidden"},
		{"missing", jsonResponse(404, testToken), nil, "404 Not Found"},
		{"rate limit", jsonResponse(429, testToken), nil, "429 Too Many Requests"},
		{"transport", nil, errors.New("Authorization: Bearer " + testToken), "request failed"},
		{"invalid JSON", jsonResponse(200, testToken), nil, "invalid API response"},
		{"oversized", jsonResponse(200, strings.Repeat("x", responseLimit+1)+testToken), nil, "exceeds size limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestService(t, nativeCredentials(), func(*http.Request) (*http.Response, error) { return tc.response, tc.err })
			token, account, err := s.Credentials(context.Background(), "member-1")
			if err == nil || !strings.Contains(err.Error(), tc.want) || token != "" || account != (Account{}) {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(err.Error(), testToken) {
				t.Fatal("secret leaked")
			}
		})
	}
}

func TestCancellationReachesTransport(t *testing.T) {
	s, _ := newTestService(t, nativeCredentials(), func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.Credentials(ctx, "member-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestPaginationRejectsUntrustedOrNonAdvancingNextLinks(t *testing.T) {
	for _, link := range []string{
		`<https://attacker.example/user/repos?per_page=100&page=2>; rel="next"`,
		`<http://api.github.com/user/repos?per_page=100&page=2>; rel="next"`,
		`<https://api.github.com/user/repos?per_page=100&page=1>; rel="next"`,
		`<https://api.github.com/user/repos?per_page=30&page=2>; rel="next"`,
		`<https://api.github.com/repos?per_page=100&page=2>; rel="next"`,
		`<https://api.github.com/user/repos?per_page=100&page=bad>; rel="next"`,
	} {
		if _, err := nextPage([]string{link}, 1); err == nil {
			t.Fatalf("accepted unsafe pagination: %s", link)
		}
	}
	if next, err := nextPage([]string{`<https://api.github.com/user/repos?per_page=100&page=7>; rel="next"`}, 1); err != nil || next != 7 {
		t.Fatalf("real next page ignored: %d %v", next, err)
	}
	if next, err := nextPage(nil, 1); err != nil || next != 0 {
		t.Fatalf("invented next page: %d %v", next, err)
	}
}

func TestRepositoryPageBoundaryDoesNotInventOrSilentlyTruncate(t *testing.T) {
	for _, count := range []int{100, 101} {
		record := `{"id":1,"full_name":"org/repo","name":"repo","default_branch":"main"}`
		body := "[" + strings.TrimSuffix(strings.Repeat(record+",", count), ",") + "]"
		s, _ := newTestService(t, nativeCredentials(), func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/user" {
				return accountResponse(), nil
			}
			return jsonResponse(http.StatusOK, body), nil
		})
		page, err := s.Repositories(context.Background(), "member-1", 1)
		if count == 100 {
			if err != nil || len(page.Repositories) != 100 || page.NextPage != 0 {
				t.Fatalf("full final page lost or invented pagination: count=%d next=%d err=%v", len(page.Repositories), page.NextPage, err)
			}
		} else if err == nil {
			t.Fatal("oversized repository page silently accepted")
		}
	}
}

func TestRepositoryRejectsInvalidOrMismatchedMetadata(t *testing.T) {
	for _, body := range []string{
		`{"id":0,"full_name":"org/repo","name":"repo"}`,
		`{"id":1,"full_name":"org/other","name":"other"}`,
		`{"id":1,"full_name":"org/repo","name":"different"}`,
		`{"id":1,"full_name":"org/../repo","name":"repo"}`,
		`null`,
	} {
		s, _ := newTestService(t, nativeCredentials(), func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/user" {
				return accountResponse(), nil
			}
			return jsonResponse(http.StatusOK, body), nil
		})
		account, repo, err := s.Repository(context.Background(), "member-1", "org/repo")
		if err == nil || account != (Account{}) || repo != (Repository{}) {
			t.Fatal("invalid repository metadata exposed")
		}
	}
}
