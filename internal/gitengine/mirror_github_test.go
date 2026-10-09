package gitengine

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestGitHubFetchCredentialEnvironmentAndRedactedErrors(t *testing.T) {
	e := newUnitEngine(t)
	const token = "fixture-native-token"
	const source = "https://github.com/acme/private.git"
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	record := filepath.Join(t.TempDir(), "args")
	git := filepath.Join(t.TempDir(), "git")
	// This child checks the actual environment without writing credentials to a
	// fixture artifact. Its deliberately hostile error output exercises redaction.
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + shellQuoteMirror(record) + "\n" +
		"[ \"$GIT_CONFIG_COUNT\" = 3 ] || exit 20\n" +
		"[ \"$GIT_CONFIG_KEY_0\" = http.extraHeader ] && [ -z \"$GIT_CONFIG_VALUE_0\" ] || exit 21\n" +
		"[ \"$GIT_CONFIG_KEY_1\" = http." + source + ".extraHeader ] || exit 22\n" +
		"[ \"$GIT_CONFIG_VALUE_1\" = " + shellQuoteMirror("Authorization: Basic "+encoded) + " ] || exit 23\n" +
		"[ \"$GIT_CONFIG_KEY_2\" = credential.helper ] && [ -z \"$GIT_CONFIG_VALUE_2\" ] || exit 24\n" +
		"[ -z \"$GH_TOKEN$GITHUB_TOKEN$GIT_TRACE$GIT_CONFIG_PARAMETERS\" ] || exit 25\n" +
		"printf '%s\\n' 'authentication failed' \"$GIT_CONFIG_VALUE_1\" " + shellQuoteMirror(token) + " >&2\nexit 1\n"
	if err := os.WriteFile(git, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "unrelated-host-token")
	t.Setenv("GITHUB_TOKEN", "unrelated-host-token")
	t.Setenv("GIT_TRACE", "1")
	t.Setenv("GIT_CONFIG_PARAMETERS", "host-config")
	e.cfg.GitPath = git
	e.cfg.MirrorResolve = func(_ context.Context, host string) ([]net.IP, error) {
		if host != "github.com" {
			t.Fatal("credential fetch resolved another host")
		}
		return []net.IP{net.ParseIP("140.82.112.3")}, nil
	}
	req := MirrorRequest{SourceURL: source, Branch: "main", Auth: domain.MirrorAuthGitHub, GitHubToken: token}
	err := e.fetchMirror(t.Context(), t.TempDir(), req, "refs/aether/incoming/test")
	var failure *mirrorFetchFailure
	if !errors.As(err, &failure) || classifyFetchError(err) != MirrorErrorAuthFailed || !strings.Contains(failure.output, "[REDACTED]") {
		t.Fatalf("credential transport did not reach expected sanitized auth failure: %v", err)
	}
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if strings.Contains(cause.Error(), token) || strings.Contains(cause.Error(), encoded) {
			t.Fatal("fetch error chain retained a credential")
		}
	}
	args, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(args), token) || strings.Contains(string(args), encoded) || strings.Contains(string(args), "Authorization") {
		t.Fatal("fetch credential escaped into argv")
	}
	for _, required := range []string{"http.followRedirects=false", "http." + source + ".followRedirects=false", "http.curloptResolve=github.com:443:140.82.112.3", "--no-write-fetch-head", "--no-recurse-submodules"} {
		if !strings.Contains(string(args), required) {
			t.Errorf("fetch is missing protection %q", required)
		}
	}
}

func TestGitHubFetchRejectsMissingCredentialBeforeTransport(t *testing.T) {
	e := newUnitEngine(t)
	e.cfg.MirrorFetch = func(context.Context, string, MirrorRequest, string) error {
		t.Fatal("transport invoked with invalid GitHub credential")
		return nil
	}
	for _, token := range []string{"", "bad\nheader", "bad\x00token", "bad token"} {
		err := e.fetchMirror(t.Context(), t.TempDir(), MirrorRequest{
			SourceURL: "https://github.com/acme/private.git", Branch: "main", Auth: domain.MirrorAuthGitHub, GitHubToken: token,
		}, "refs/aether/incoming/test")
		if classifyFetchError(err) != MirrorErrorAuthFailed {
			t.Fatalf("invalid credential did not fail closed: %v", err)
		}
	}
}

func TestGitHubMirrorCannotUseLocalTransportURL(t *testing.T) {
	for _, source := range []string{"/tmp/source", "https://other.example/acme/private.git", "https://github.com:443/acme/private.git", "https://github.com/acme/private.git?token=x"} {
		if err := validateMirrorRequest(MirrorRequest{SourceURL: source, Branch: "main", Auth: domain.MirrorAuthGitHub}, true); err == nil {
			t.Errorf("GitHub source validation accepted %q even with a test transport", source)
		}
	}
}

func TestGitHubPrivateRepositoryAccessFailureIsAuthenticationFailure(t *testing.T) {
	for _, output := range []string{"remote: Repository not found.", "fatal: the requested URL returned error: 404"} {
		failure := &mirrorFetchFailure{err: errors.New("exit status 128"), output: output, github: true}
		if kind := classifyFetchError(failure); kind != MirrorErrorAuthFailed {
			t.Fatalf("GitHub hidden repository failure = %s", kind)
		}
		failure.github = false
		if kind := classifyFetchError(failure); kind != MirrorErrorFailed {
			t.Fatalf("changed generic repository failure semantics = %s", kind)
		}
	}
}
