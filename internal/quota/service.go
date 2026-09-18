// Package quota reads the read-only subscription quota exposed by Claude Code
// and Codex OAuth accounts. It deliberately does not refresh credentials or
// import the wire protocol package.
package quota

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/memberhome"
)

const (
	claudeCredentialFile = ".claude/.credentials.json"
	codexCredentialFile  = ".codex/auth.json"
	claudeUsageEndpoint  = "https://api.anthropic.com/api/oauth/usage"
	codexUsageEndpoint   = "https://chatgpt.com/backend-api/wham/usage"
	maxCredentialBytes   = 1 << 20
	maxUsageBodyBytes    = 256 << 10
	cacheTTL             = 60 * time.Second
	requestTimeout       = 45 * time.Second
	requestFloor         = 10 * time.Second
	errorRetryFloor      = 60 * time.Second
)

var usageHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	// Quota endpoints are fixed vendor HTTPS origins. A redirect must not
	// carry an Authorization header to an arbitrary host.
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
	// Leave the standard transport in place so deployment-scoped
	// HTTP(S)_PROXY settings work for these fixed vendor origins without
	// introducing a second transport policy.
	Transport: http.DefaultTransport,
}

// Provider is the provider-neutral quota result consumed by the RPC layer.
type Provider struct {
	Provider  string
	Status    string
	Windows   []Window
	Plan      string
	UpdatedAt *time.Time
	CheckedAt time.Time
	RetryAt   *time.Time
	Error     string
}

// Window is one vendor-reported rolling quota window. UsedPercent is retained
// only when the vendor actually reported a finite value in [0, 100].
type Window struct {
	ID          string
	Label       string
	UsedPercent float64
	ResetsAt    *time.Time
}

// Service reads quota for member homes. Credential bytes are never retained in
// this object; only a digest identifies the credential used for a cache entry.
type Service struct {
	homes *memberhome.Manager

	mu     sync.Mutex
	cache  map[cacheKey]cacheState
	flight map[string]*readFlight
	now    func() time.Time
}

type cacheKey struct {
	account  string
	provider string
}

type cacheState struct {
	fingerprint   string
	provider      Provider
	success       bool
	fetchedAt     time.Time
	lastAttempt   time.Time
	retryAt       time.Time
	failureStatus string
	failureError  string
}

type readFlight struct {
	done        chan struct{}
	providers   []Provider
	fingerprint [2]string
	err         error
}

// New returns a quota service using homes for read-only credential access.
func New(homes *memberhome.Manager) *Service {
	return &Service{
		homes:  homes,
		cache:  make(map[cacheKey]cacheState),
		flight: make(map[string]*readFlight),
		now:    time.Now,
	}
}

type credential struct {
	status      string
	err         string
	token       string
	accountID   string
	plan        string
	fingerprint string
}

type credentials struct {
	claude credential
	codex  credential
}

// Read returns both supported provider rows, including explicit rows for
// missing, unsupported, unavailable, or malformed credentials. Calls for an
// account are coalesced; a cancelled waiter never cancels another caller's
// request.
func (s *Service) Read(ctx context.Context, account domain.MemberID, refresh bool) ([]Provider, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.homes == nil {
		return nil, errors.New("quota: member homes are not configured")
	}
	if account == "" {
		return nil, errors.New("quota: account is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	key := string(account)

	s.mu.Lock()
	if existing := s.flight[key]; existing != nil {
		s.mu.Unlock()
		select {
		case <-existing.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if existing.err != nil {
			return nil, existing.err
		}
		return cloneProviders(existing.providers), nil
	}
	flight := &readFlight{done: make(chan struct{})}
	s.flight[key] = flight
	s.mu.Unlock()

	requestCtx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	creds, readErr := s.readCredentials(requestCtx, account)
	var providers []Provider
	if readErr == nil {
		providers, readErr = s.readLeader(requestCtx, account, creds, refresh)
	}
	if readErr == nil {
		latest, latestErr := s.readCredentials(requestCtx, account)
		if latestErr != nil {
			readErr = latestErr
		} else if latest.fingerprints() != creds.fingerprints() {
			providers = s.statusRows(latest, "credential changed while usage was fetched")
		}
		if readErr == nil {
			flight.fingerprint = latest.fingerprints()
		}
	}
	flight.providers = cloneProviders(providers)
	flight.err = readErr
	close(flight.done)

	s.mu.Lock()
	delete(s.flight, key)
	s.mu.Unlock()
	if readErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, readErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return cloneProviders(providers), nil
}

func (s *Service) readLeader(ctx context.Context, account domain.MemberID, creds credentials, refresh bool) ([]Provider, error) {
	claude, err := s.readProvider(ctx, account, creds.claude, "claude", refresh)
	if err != nil {
		return nil, err
	}
	codex, err := s.readProvider(ctx, account, creds.codex, "codex", refresh)
	if err != nil {
		return nil, err
	}
	return []Provider{claude, codex}, nil
}

func (s *Service) readCredentials(ctx context.Context, account domain.MemberID) (credentials, error) {
	if err := ctx.Err(); err != nil {
		return credentials{}, err
	}
	claudeBytes, claudeErr := s.homes.ReadCredential(account, claudeCredentialFile, maxCredentialBytes)
	if err := ctx.Err(); err != nil {
		return credentials{}, err
	}
	codexBytes, codexErr := s.homes.ReadCredential(account, codexCredentialFile, maxCredentialBytes)
	if err := ctx.Err(); err != nil {
		return credentials{}, err
	}
	return credentials{
		claude: parseClaudeCredential(claudeBytes, claudeErr, s.now()),
		codex:  parseCodexCredential(codexBytes, codexErr, s.now()),
	}, nil
}

func (c credentials) fingerprints() [2]string {
	return [2]string{c.claude.fingerprint, c.codex.fingerprint}
}

func (s *Service) readProvider(ctx context.Context, account domain.MemberID, cred credential, provider string, refresh bool) (Provider, error) {
	now := s.now().UTC()
	key := cacheKey{account: string(account), provider: provider}
	state := s.prepareState(key, cred.fingerprint)
	if cred.status != "ok" {
		s.clearState(key, cred.fingerprint)
		return credentialProvider(provider, cred, now), nil
	}

	if state.success && !state.retryAt.IsZero() {
		if now.Before(state.retryAt) {
			return staleProvider(state.provider, now, state.failureError, state.retryAt), nil
		}
	}
	if state.success && state.retryAt.IsZero() && !refresh && now.Sub(state.fetchedAt) < cacheTTL && !windowsExpired(state.provider.Windows, now) {
		return cloneProvider(state.provider), nil
	}
	if state.success && windowsExpired(state.provider.Windows, now) && now.Sub(state.lastAttempt) < requestFloor {
		return staleProvider(state.provider, now, "quota window elapsed; refresh is cooling down", addDuration(now, requestFloor)), nil
	}
	if !state.retryAt.IsZero() && now.Before(state.retryAt) {
		return cachedOrUnavailable(state, provider, cred, now, "provider requested a retry later", state.retryAt), nil
	}
	if !state.lastAttempt.IsZero() && now.Before(state.lastAttempt.Add(requestFloor)) {
		return cachedOrUnavailable(state, provider, cred, now, "quota request cooldown", state.lastAttempt.Add(requestFloor)), nil
	}

	s.markAttempt(key, cred.fingerprint, now)
	var result usageResult
	var err error
	if provider == "claude" {
		result, err = fetchClaude(ctx, cred.token)
	} else {
		result, err = fetchCodex(ctx, cred.token, cred.accountID)
	}
	if err != nil {
		if ctx.Err() != nil {
			return Provider{}, ctx.Err()
		}
		status, safeErr, retryAt := classifyFetchError(err, s.now().UTC())
		s.markFailure(key, cred.fingerprint, status, safeErr, retryAt)
		state = s.getState(key, cred.fingerprint)
		if state.success {
			return staleProvider(state.provider, now, state.failureError, state.retryAt), nil
		}
		return failedProvider(provider, cred.plan, status, now, retryAt, safeErr), nil
	}

	plan := result.plan
	if plan == "" {
		plan = cred.plan
	}
	measured := Provider{
		Provider:  provider,
		Status:    "ok",
		Windows:   cloneWindows(result.windows),
		Plan:      plan,
		UpdatedAt: pointerTime(now),
		CheckedAt: now,
	}
	s.markSuccess(key, cred.fingerprint, measured, now)
	return cloneProvider(measured), nil
}

type usageResult struct {
	windows []Window
	plan    string
}

func (s *Service) prepareState(key cacheKey, fingerprint string) cacheState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.cache[key]
	if !ok || state.fingerprint != fingerprint {
		state = cacheState{fingerprint: fingerprint}
		s.cache[key] = state
	}
	return state
}

func (s *Service) clearState(key cacheKey, fingerprint string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache[key] = cacheState{fingerprint: fingerprint}
}

func (s *Service) getState(key cacheKey, fingerprint string) cacheState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.cache[key]
	if state.fingerprint != fingerprint {
		return cacheState{fingerprint: fingerprint}
	}
	return state
}

func (s *Service) markAttempt(key cacheKey, fingerprint string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.cache[key]
	if state.fingerprint == fingerprint {
		state.lastAttempt = at
		s.cache[key] = state
	}
}

func (s *Service) markFailure(key cacheKey, fingerprint, status, reason string, retryAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.cache[key]
	if state.fingerprint == fingerprint {
		state.retryAt = retryAt
		state.failureStatus = status
		state.failureError = reason
		s.cache[key] = state
	}
}

func (s *Service) markSuccess(key cacheKey, fingerprint string, provider Provider, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state := s.cache[key]; state.fingerprint == fingerprint {
		state.provider = cloneProvider(provider)
		state.success = true
		state.fetchedAt = at
		state.lastAttempt = at
		state.retryAt = time.Time{}
		state.failureStatus = ""
		state.failureError = ""
		s.cache[key] = state
	}
}

func credentialProvider(provider string, cred credential, now time.Time) Provider {
	status := cred.status
	if status == "" {
		status = "error"
	}
	return Provider{Provider: provider, Status: status, Plan: cred.plan, CheckedAt: now, Error: cred.err}
}

func failedProvider(provider, plan, status string, now time.Time, retryAt time.Time, errText string) Provider {
	result := Provider{Provider: provider, Status: status, Plan: plan, CheckedAt: now, Error: errText}
	if !retryAt.IsZero() {
		result.RetryAt = pointerTime(retryAt)
	}
	return result
}

func cachedOrUnavailable(state cacheState, provider string, cred credential, now time.Time, reason string, retryAt time.Time) Provider {
	if state.failureError != "" {
		if state.success {
			return staleProvider(state.provider, now, state.failureError, state.retryAt)
		}
		status := state.failureStatus
		if status == "" {
			status = "unavailable"
		}
		return failedProvider(provider, cred.plan, status, now, state.retryAt, state.failureError)
	}
	if state.success {
		return staleProvider(state.provider, now, reason, retryAt)
	}
	return failedProvider(provider, cred.plan, "unavailable", now, retryAt, reason)
}

func staleProvider(cached Provider, now time.Time, reason string, retryAt time.Time) Provider {
	result := cloneProvider(cached)
	result.Status = "stale"
	result.CheckedAt = now
	result.Error = reason
	result.RetryAt = pointerIfTime(retryAt)
	filtered := result.Windows[:0]
	for _, window := range result.Windows {
		if window.ResetsAt != nil && !now.Before(*window.ResetsAt) {
			continue
		}
		filtered = append(filtered, window)
	}
	result.Windows = filtered
	return result
}

func windowsExpired(windows []Window, now time.Time) bool {
	for _, window := range windows {
		if window.ResetsAt != nil && !now.Before(*window.ResetsAt) {
			return true
		}
	}
	return false
}

func (s *Service) statusRows(creds credentials, reason string) []Provider {
	now := s.now().UTC()
	return []Provider{
		statusRow("claude", creds.claude, now, reason),
		statusRow("codex", creds.codex, now, reason),
	}
}

func statusRow(provider string, cred credential, now time.Time, reason string) Provider {
	if cred.status != "ok" {
		return credentialProvider(provider, cred, now)
	}
	return failedProvider(provider, cred.plan, "unavailable", now, now.Add(errorRetryFloor), reason)
}

func parseJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON")
		}
		return err
	}
	return nil
}

func fingerprint(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = io.WriteString(h, part)
		_, _ = io.WriteString(h, "\x00")
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sortedScopes(scopes []string) string {
	copyScopes := append([]string(nil), scopes...)
	sort.Strings(copyScopes)
	return strings.Join(copyScopes, "\x00")
}

func pointerTime(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func addDuration(now time.Time, duration time.Duration) time.Time {
	return now.Add(duration)
}

func pointerIfTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return pointerTime(value)
}

func cloneProviders(in []Provider) []Provider {
	out := make([]Provider, len(in))
	for i := range in {
		out[i] = cloneProvider(in[i])
	}
	return out
}

func cloneProvider(in Provider) Provider {
	out := in
	out.Windows = cloneWindows(in.Windows)
	if in.UpdatedAt != nil {
		out.UpdatedAt = pointerTime(*in.UpdatedAt)
	}
	if in.RetryAt != nil {
		out.RetryAt = pointerTime(*in.RetryAt)
	}
	return out
}

func cloneWindows(in []Window) []Window {
	if in == nil {
		return nil
	}
	out := make([]Window, len(in))
	for i := range in {
		out[i] = in[i]
		if in[i].ResetsAt != nil {
			out[i].ResetsAt = pointerTime(*in[i].ResetsAt)
		}
	}
	return out
}

func classifyFetchError(err error, now time.Time) (string, string, time.Time) {
	var httpErr *httpStatusError
	if errors.As(err, &httpErr) {
		return httpErr.status, httpErr.reason, httpErr.retryAt
	}
	var responseErr *responseError
	if errors.As(err, &responseErr) {
		return "error", responseErr.reason, now.Add(errorRetryFloor)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "unavailable", "provider request timed out", now.Add(errorRetryFloor)
	}
	return "unavailable", "provider request unavailable", now.Add(errorRetryFloor)
}

type responseError struct {
	reason string
}

func (e *responseError) Error() string { return e.reason }

type httpStatusError struct {
	status  string
	reason  string
	retryAt time.Time
}

func (e *httpStatusError) Error() string { return e.reason }

func safeHTTPError(status int, headers http.Header, now time.Time) error {
	retryAt := now.Add(errorRetryFloor)
	if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
		retryAt = retryAfter(headers.Get("Retry-After"), now)
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &httpStatusError{status: "unauthenticated", reason: "provider rejected the OAuth credential", retryAt: retryAt}
	case http.StatusTooManyRequests:
		return &httpStatusError{status: "unavailable", reason: "provider rate limit reached", retryAt: retryAt}
	default:
		return &httpStatusError{status: "error", reason: fmt.Sprintf("provider returned HTTP %d", status), retryAt: retryAt}
	}
}

func retryAfter(raw string, now time.Time) time.Time {
	deadline := now.Add(errorRetryFloor)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return deadline
	}
	if seconds, err := time.ParseDuration(raw + "s"); err == nil && seconds > 0 {
		if seconds > 7*24*time.Hour {
			seconds = 7 * 24 * time.Hour
		}
		candidate := now.Add(seconds)
		if candidate.After(deadline) {
			return candidate
		}
		return deadline
	}
	if parsed, err := http.ParseTime(raw); err == nil {
		maximum := now.Add(7 * 24 * time.Hour)
		if parsed.After(maximum) {
			return maximum
		}
		if parsed.After(deadline) {
			return parsed.UTC()
		}
	}
	return deadline
}

func readResponse(resp *http.Response) ([]byte, error) {
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUsageBodyBytes+1))
	if err != nil {
		return nil, errors.New("provider response could not be read")
	}
	if len(body) > maxUsageBodyBytes {
		return nil, errors.New("provider response exceeded size limit")
	}
	return body, nil
}
