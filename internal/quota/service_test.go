package quota

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/memberhome"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func TestReadCollectsClaudeAndCodexWithoutLeakingCredentialBodies(t *testing.T) {
	homes, member := fixtureHomes(t, `{"claudeAiOauth":{"accessToken":"claude-secret","expiresAt":4102444800000,"scopes":["user:inference"],"subscriptionType":"max"}}`, `{"tokens":{"access_token":"codex-secret","account_id":"acct-1"}}`)
	var requests atomic.Int32
	oldTransport := usageHTTPClient.Transport
	usageHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		if req.URL.String() == claudeUsageEndpoint {
			if got := req.Header.Get("Authorization"); got != "Bearer claude-secret" {
				return nil, fmt.Errorf("Claude auth header = %q", got)
			}
			if req.Header.Get("anthropic-beta") != "oauth-2025-04-20" || req.Header.Get("User-Agent") != "claude-code/2.1.0" {
				return nil, errors.New("missing Claude usage headers")
			}
			return fixtureResponse(`{"five_hour":{"utilization":12.5,"resets_at":"2100-01-01T00:00:00Z"},"seven_day":{"utilization":50,"resets_at":"2100-01-02T00:00:00Z"},"seven_day_sonnet":{"used_percentage":2,"resets_at":null}}`), nil
		}
		if req.URL.String() == codexUsageEndpoint {
			if req.Header.Get("Authorization") != "Bearer codex-secret" || req.Header.Get("ChatGPT-Account-Id") != "acct-1" {
				return nil, errors.New("missing Codex auth headers")
			}
			if req.Header.Get("User-Agent") != "codex-cli" || req.Header.Get("OpenAI-Beta") != "codex-1" || req.Header.Get("originator") != "Codex Desktop" {
				return nil, errors.New("missing Codex usage headers")
			}
			return fixtureResponse(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":30,"limit_window_seconds":1800,"reset_at":2100000000},"secondary_window":{"used_percent":4,"limit_window_seconds":604800,"reset_at":2100000100}}}`), nil
		}
		return nil, fmt.Errorf("unexpected URL %s", req.URL)
	})
	t.Cleanup(func() { usageHTTPClient.Transport = oldTransport })

	service := New(homes)
	providers, err := service.Read(context.Background(), member, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 {
		t.Fatalf("providers = %d, want 2", len(providers))
	}
	if providers[0].Provider != "claude" || providers[0].Status != "ok" || providers[0].Plan != "max" || len(providers[0].Windows) != 3 {
		t.Fatalf("Claude provider = %#v", providers[0])
	}
	if providers[0].Windows[0].UsedPercent != 12.5 || providers[0].Windows[2].UsedPercent != 2 || providers[0].Windows[2].ResetsAt != nil {
		t.Fatalf("Claude windows = %#v", providers[0].Windows)
	}
	if providers[1].Provider != "codex" || providers[1].Status != "ok" || providers[1].Plan != "plus" || len(providers[1].Windows) != 2 {
		t.Fatalf("Codex provider = %#v", providers[1])
	}
	if requests.Load() != 2 {
		t.Fatalf("request count = %d, want 2", requests.Load())
	}
	providers[0].Windows[0].UsedPercent = 99
	cached, err := service.Read(context.Background(), member, false)
	if err != nil {
		t.Fatal(err)
	}
	if cached[0].Windows[0].UsedPercent != 12.5 {
		t.Fatalf("cached result was mutable through caller: %#v", cached[0].Windows)
	}
}

func TestReadHonorsIdentityChangeAndDoesNotServeOldCache(t *testing.T) {
	homes, member := fixtureHomes(t, `{"claudeAiOauth":{"accessToken":"old-token","expiresAt":4102444800000,"scopes":["user:inference"]}}`, ``)
	var authHeaders []string
	var mu sync.Mutex
	oldTransport := usageHTTPClient.Transport
	usageHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		authHeaders = append(authHeaders, req.Header.Get("Authorization"))
		mu.Unlock()
		return fixtureResponse(`{"five_hour":{"utilization":10,"resets_at":"2100-01-01T00:00:00Z"}}`), nil
	})
	t.Cleanup(func() { usageHTTPClient.Transport = oldTransport })

	service := New(homes)
	if _, err := service.Read(context.Background(), member, false); err != nil {
		t.Fatal(err)
	}
	home, err := homes.Path(member)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, claudeCredentialFile), []byte(`{"claudeAiOauth":{"accessToken":"new-token","expiresAt":4102444800000,"scopes":["user:inference"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err := service.Read(context.Background(), member, false); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(authHeaders) != 2 || authHeaders[0] != "Bearer old-token" || authHeaders[1] != "Bearer new-token" {
		t.Fatalf("auth headers = %#v", authHeaders)
	}
}

func TestReadStaleDropsWindowsAfterResetAndHonorsRetryAfter(t *testing.T) {
	homes, member := fixtureHomes(t, `{"claudeAiOauth":{"accessToken":"token","expiresAt":4102444800000,"scopes":["user:inference"]}}`, ``)
	var requests atomic.Int32
	oldTransport := usageHTTPClient.Transport
	usageHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch requests.Add(1) {
		case 1:
			return fixtureResponse(`{"five_hour":{"utilization":25,"resets_at":"2026-09-18T12:05:00Z"}}`), nil
		default:
			return fixtureResponseWithStatus(http.StatusTooManyRequests, `secret provider body`, http.Header{"Retry-After": []string{"120"}}), nil
		}
	})
	t.Cleanup(func() { usageHTTPClient.Transport = oldTransport })

	current := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	service := New(homes)
	service.now = func() time.Time { return current }
	if got, err := service.Read(context.Background(), member, false); err != nil || got[0].Status != "ok" {
		t.Fatalf("initial read = %#v, %v", got, err)
	}
	current = current.Add(6 * time.Minute)
	got, err := service.Read(context.Background(), member, true)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Status != "stale" || len(got[0].Windows) != 0 || got[0].RetryAt == nil {
		t.Fatalf("stale read = %#v", got[0])
	}
	if strings.Contains(got[0].Error, "secret") {
		t.Fatalf("provider body leaked in error: %q", got[0].Error)
	}
	if requests.Load() != 2 {
		t.Fatalf("request count = %d, want 2", requests.Load())
	}
	retryAt := *got[0].RetryAt
	safeError := got[0].Error
	current = retryAt.Add(-time.Second)
	later, err := service.Read(context.Background(), member, false)
	if err != nil {
		t.Fatal(err)
	}
	if later[0].Status != "stale" || later[0].Error != safeError || later[0].RetryAt == nil || !later[0].RetryAt.Equal(retryAt) {
		t.Fatalf("stale retry result = %#v, want status/error/retry_at preserved", later[0])
	}
	if requests.Load() != 2 {
		t.Fatalf("retry cooldown did not suppress request: %d", requests.Load())
	}
}

func TestSafeHTTPErrorHonorsRetryAfter503(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	status, _, retryAt := classifyFetchError(safeHTTPError(http.StatusServiceUnavailable, http.Header{"Retry-After": []string{"120"}}, now), now)
	if status != "error" || !retryAt.Equal(now.Add(120*time.Second)) {
		t.Fatalf("503 classification = (%q, %v), want error and 120-second retry", status, retryAt)
	}
}

func TestReadPreservesInitialFailureDuringRetryCooldown(t *testing.T) {
	homes, member := fixtureHomes(t, `{"claudeAiOauth":{"accessToken":"token","expiresAt":4102444800000,"scopes":["user:inference"]}}`, ``)
	var requests atomic.Int32
	oldTransport := usageHTTPClient.Transport
	usageHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return fixtureResponseWithStatus(http.StatusUnauthorized, `secret provider body`, nil), nil
	})
	t.Cleanup(func() { usageHTTPClient.Transport = oldTransport })
	current := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	service := New(homes)
	service.now = func() time.Time { return current }
	first, err := service.Read(context.Background(), member, true)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Status != "unauthenticated" || first[0].RetryAt == nil {
		t.Fatalf("initial failure = %#v", first[0])
	}
	retryAt := *first[0].RetryAt
	safeError := first[0].Error
	current = retryAt.Add(-time.Second)
	second, err := service.Read(context.Background(), member, false)
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Status != "unauthenticated" || second[0].Error != safeError || second[0].RetryAt == nil || !second[0].RetryAt.Equal(retryAt) {
		t.Fatalf("cooldown failure = %#v, want persisted failure", second[0])
	}
	if requests.Load() != 1 {
		t.Fatalf("request count = %d, want one request", requests.Load())
	}
}

func TestReadRejectsQuotaWithNoValidWindows(t *testing.T) {
	homes, member := fixtureHomes(t, `{"claudeAiOauth":{"accessToken":"token","expiresAt":4102444800000,"scopes":["user:inference"]}}`, ``)
	oldTransport := usageHTTPClient.Transport
	usageHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return fixtureResponse(`{"five_hour":{"utilization":null,"resets_at":null},"seven_day":{"utilization":101,"resets_at":"2100-01-01T00:00:00Z"}}`), nil
	})
	t.Cleanup(func() { usageHTTPClient.Transport = oldTransport })
	got, err := New(homes).Read(context.Background(), member, true)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Status != "error" || len(got[0].Windows) != 0 {
		t.Fatalf("invalid quota result = %#v", got[0])
	}
}

func TestCredentialParsingRecognizesAPIKeysScopesAndMalformedJSON(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"Claude API key", []byte(`{"apiKey":"api-key"}`), "unsupported"},
		{"Claude scope", []byte(`{"claudeAiOauth":{"accessToken":"token","expiresAt":4102444800000,"scopes":["user:profile"]}}`), "unauthenticated"},
		{"Claude malformed", []byte(`{"claudeAiOauth":{"accessToken":"super-secret"`), "error"},
		{"Codex API key", []byte(`{"OPENAI_API_KEY":"api-key"}`), "unsupported"},
		{"Codex missing account", []byte(`{"tokens":{"access_token":"token"}}`), "unauthenticated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got credential
			if strings.HasPrefix(tc.name, "Claude") {
				got = parseClaudeCredential(tc.data, nil, now)
			} else {
				got = parseCodexCredential(tc.data, nil, now)
			}
			if got.status != tc.want {
				t.Fatalf("status = %q, want %q", got.status, tc.want)
			}
			if strings.Contains(got.err, "api-key") || strings.Contains(got.err, "token") || strings.Contains(got.err, "super-secret") {
				t.Fatalf("credential secret leaked in error: %q", got.err)
			}
		})
	}
}

func TestReadCoalescesRequestsAndCanceledLeaderDoesNotCancelFlight(t *testing.T) {
	homes, member := fixtureHomes(t, `{"claudeAiOauth":{"accessToken":"token","expiresAt":4102444800000,"scopes":["user:inference"]}}`, ``)
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	var requests atomic.Int32
	oldTransport := usageHTTPClient.Transport
	usageHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		startedOnce.Do(func() { close(started) })
		<-release
		return fixtureResponse(`{"five_hour":{"utilization":20,"resets_at":"2100-01-01T00:00:00Z"}}`), nil
	})
	t.Cleanup(func() { usageHTTPClient.Transport = oldTransport })

	service := New(homes)
	leaderCtx, cancel := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := service.Read(leaderCtx, member, true)
		leaderDone <- err
	}()
	<-started
	followerDone := make(chan error, 1)
	go func() {
		_, err := service.Read(context.Background(), member, true)
		followerDone <- err
	}()
	for range 1000 {
		service.mu.Lock()
		joined := service.flight[string(member)] != nil
		service.mu.Unlock()
		if joined {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	close(release)
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v, want context cancellation", err)
	}
	if err := <-followerDone; err != nil {
		t.Fatalf("follower error = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("request count = %d, want one coalesced request", requests.Load())
	}
}

func fixtureHomes(t *testing.T, claudeJSON, codexJSON string) (*memberhome.Manager, domain.MemberID) {
	t.Helper()
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatal(err)
	}
	member := domain.MemberID("member-1")
	home, err := homes.Path(member)
	if err != nil {
		t.Fatal(err)
	}
	if claudeJSON != "" {
		if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, claudeCredentialFile), []byte(claudeJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if codexJSON != "" {
		if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, codexCredentialFile), []byte(codexJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return homes, member
}

func fixtureResponse(body string) *http.Response {
	return fixtureResponseWithStatus(http.StatusOK, body, nil)
}

func fixtureResponseWithStatus(status int, body string, headers http.Header) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     headers,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
