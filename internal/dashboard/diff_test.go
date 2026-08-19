package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type fakePatcher struct {
	calls atomic.Int32
	limit atomic.Int32
	patch gitengine.Patch
	err   error
}

func (f *fakePatcher) RunPatch(_ context.Context, _ domain.RunID, maxBytes int) (gitengine.Patch, error) {
	f.calls.Add(1)
	f.limit.Store(int32(maxBytes))
	return f.patch, f.err
}

// get issues an authenticated GET the way the SPA does.
func (e *env) get(token, path string) (int, []byte) {
	e.t.Helper()
	req, err := http.NewRequestWithContext(e.t.Context(), http.MethodGet, e.base+path, nil)
	if err != nil {
		e.t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("get %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, out
}

// TestPatchEndpointGatesOnTheRunTheMemberMaySee is the diff timeline's
// server-side contract: patch text is a read of someone's working tree, so
// it goes out only behind a token, only for a run the control channel would
// have shown that member, and only up to the byte ceiling.
func TestPatchEndpointGatesOnTheRunTheMemberMaySee(t *testing.T) {
	e := newEnv(t)
	path := "/api/v1/run/" + string(e.run.ID) + "/patch"

	// Nothing is wired to render diffs yet: the endpoint says so rather than
	// pretending the run has no changes.
	token := e.mint(e.admin)
	status, body := e.get(token, path)
	if status != http.StatusServiceUnavailable || e.errorCode(body) != protocol.CodeUnavailable {
		t.Fatalf("patch without a git engine = %d %s, want 503 and code %d", status, body, protocol.CodeUnavailable)
	}

	// Set before the first request that needs it; the gateway has read no
	// config on any other goroutine.
	git := &fakePatcher{patch: gitengine.Patch{Base: "abc123", Text: "diff --git a/x b/x\n", Truncated: true}}
	e.gw.cfg.Git = git

	if status, body = e.get("", path); status != http.StatusUnauthorized {
		t.Fatalf("patch without a token = %d %s, want 401", status, body)
	}
	if git.calls.Load() != 0 {
		t.Fatal("an unauthenticated request reached the git engine")
	}

	status, body = e.get(token, path)
	if status != http.StatusOK {
		t.Fatalf("patch status = %d, want 200 (%s)", status, body)
	}
	var got patchResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if got.RunID != string(e.run.ID) || got.Base != "abc123" || !got.Truncated ||
		got.Patch != "diff --git a/x b/x\n" {
		t.Fatalf("patch body = %+v", got)
	}
	if int(git.limit.Load()) != maxPatchBytes {
		t.Errorf("render limit = %d, want the endpoint's ceiling %d", git.limit.Load(), maxPatchBytes)
	}

	// A member the server no longer trusts - here one dropped back to
	// pending approval after its token was minted - is refused by the same
	// per-request gate run.get applies, before anything is rendered.
	stranger := &domain.Member{DisplayName: "stranger", Role: domain.RoleCollaborator, PublicKey: testKey(t)}
	if err := e.db.CreateMember(t.Context(), stranger); err != nil {
		t.Fatalf("create member: %v", err)
	}
	strangerToken := e.mint(stranger.ID)
	stranger.Pending = true
	if err := e.db.UpdateMember(t.Context(), stranger); err != nil {
		t.Fatalf("update member: %v", err)
	}
	before := git.calls.Load()
	status, body = e.get(strangerToken, path)
	if status != http.StatusForbidden || e.errorCode(body) != protocol.CodeDenied {
		t.Fatalf("pending member patch = %d %s, want 403 and code %d", status, body, protocol.CodeDenied)
	}
	if got := git.calls.Load(); got != before {
		t.Fatal("a denied request reached the git engine")
	}

	status, body = e.get(token, "/api/v1/run/run_missing/patch")
	if status != http.StatusNotFound || e.errorCode(body) != protocol.CodeNotFound {
		t.Fatalf("unknown run patch = %d %s, want 404 and code %d", status, body, protocol.CodeNotFound)
	}

	// A checkout that is gone answers unavailable without echoing the
	// server-side path the wrapped error carries.
	git.err = errors.New("gitengine: run run_1 has no identity record: open /srv/aether/checkouts/run_1.json: no such file")
	status, body = e.get(token, path)
	if status != http.StatusServiceUnavailable || e.errorCode(body) != protocol.CodeUnavailable {
		t.Fatalf("missing checkout = %d %s, want 503 and code %d", status, body, protocol.CodeUnavailable)
	}
	if strings.Contains(string(body), "/srv/aether") {
		t.Errorf("the failure leaked a server path: %s", body)
	}
}
