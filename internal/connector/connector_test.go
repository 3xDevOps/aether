package connector

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/oauth2"
	"golang.org/x/time/rate"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/ops"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

// TestConnectorEndToEnd drives the real handler over HTTP with go-sdk's
// MCP and OAuth client, against a real sshd.Server and store: discovery
// from a 401, a client_id metadata document the test serves, an untrusted
// client, the consent page with a wrong and then the right pairing code,
// the PKCE token exchange, the tool list and calls, refresh rotation,
// connector.revoke, a refresh token presented again within a minute of
// its rotation and later, and a revoked minting device.
func TestConnectorEndToEnd(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()

	status, header, body := e.bearer("")
	metadata := e.origin + "/.well-known/oauth-protected-resource" + strings.TrimPrefix(e.base, e.origin)
	if status != http.StatusUnauthorized || !strings.Contains(header.Get("WWW-Authenticate"), `resource_metadata="`+metadata+`"`) {
		t.Fatalf("no token = %d %q %s, want 401 naming %s", status, header.Get("WWW-Authenticate"), body, metadata)
	}
	var asm map[string]any
	getJSON(t, e.origin+"/.well-known/oauth-authorization-server"+strings.TrimPrefix(e.base, e.origin), &asm)
	if _, ok := asm["jwks_uri"]; ok || asm["issuer"] != e.base || fmt.Sprint(asm["code_challenge_methods_supported"]) != "[S256]" ||
		asm["client_id_metadata_document_supported"] != true || asm["registration_endpoint"] != nil {
		t.Fatalf("authorization server metadata = %v", asm)
	}

	e.edge.version.Store(1)
	if err := e.device.Call(protocol.MethodConnectorCode, struct{}{}, nil); err == nil || !strings.Contains(err.Error(), "speaks edge protocol 1") {
		t.Fatalf("connector.code through a version-1 edge = %v", err)
	}
	e.edge.version.Store(2)

	authorize := func(clientID string) string {
		return e.base + "/authorize?" + url.Values{"response_type": {"code"}, "client_id": {clientID},
			"redirect_uri": {e.redirect}, "code_challenge": {oauth2.S256ChallengeFromVerifier(oauth2.GenerateVerifier())},
			"code_challenge_method": {"S256"}, "resource": {e.base}}.Encode()
	}
	resp, err := http.Get(authorize("https://evil.example/client.json"))
	if err != nil {
		t.Fatal(err)
	}
	if page, _ := readBody(resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(page, "evil.example is not one of them") {
		t.Fatalf("untrusted client = %d %s", resp.StatusCode, page)
	}
	granted, err := e.consent(authorize(e.clientID), e.mintCode(t), allScopes)
	if err != nil {
		t.Fatal(err)
	}
	if wrong := e.tokenRequest(t, url.Values{"grant_type": {"authorization_code"}, "client_id": {e.clientID}, "code": {granted.Code},
		"redirect_uri": {e.redirect}, "code_verifier": {oauth2.GenerateVerifier()}, "resource": {e.base}}, http.StatusBadRequest); wrong.Error != "invalid_grant" {
		t.Fatalf("exchange with the wrong code_verifier = %+v", wrong)
	}

	first := e.connect(t, allScopes...)
	// Clients may keep only the first 512 characters of the instructions.
	got := first.session.InitializeResult().Instructions
	if len(got) > 512 {
		t.Errorf("the instructions are %d characters, over 512: %q", len(got), got)
	}
	for _, want := range []string{"aether_launch_options", "aether_get_run", "aether_list_runs query", "aether_message_run", "aether_needs_attention", "untrusted"} {
		if !strings.Contains(got, want) {
			t.Errorf("the instructions lack %q: %q", want, got)
		}
	}
	var names []string
	for tool, err := range first.session.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
		a := tool.Annotations
		if a == nil || a.DestructiveHint == nil || a.OpenWorldHint == nil || tool.Title == "" || strings.Contains(tool.Description, "tier") {
			t.Errorf("%s: annotations %+v, title %q, description %q; want explicit hints and a title", tool.Name, a, tool.Title, tool.Description)
		}
	}
	slices.Sort(names)
	if want := sorted(readTools, writeTools, controlTools); !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	if out := e.callTool(t, first.session, "aether_list_workspaces", nil); !strings.Contains(out, string(e.ws.ID)) {
		t.Fatalf("aether_list_workspaces = %s", out)
	}
	e.callTool(t, first.session, "aether_launch_run", map[string]any{"workspace_id": e.ws.ID, "harness": "claude", "task": "fix the flaky test"})
	if got := e.runs.launches(); !slices.Equal(got, []string{string(e.ws.ID) + " claude fix the flaky test via Test client"}) {
		t.Fatalf("launches = %v", got)
	}

	var list protocol.ConnectorListResult
	if err := e.admin.Call(protocol.MethodConnectorList, struct{}{}, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Connectors) != 1 || list.Connectors[0].DeviceID != string(e.deviceID) || list.Connectors[0].RedirectHost != e.clientHost ||
		list.Connectors[0].ClientName != "Test client" || list.Connectors[0].LastUsedAt == "" ||
		!slices.Equal(list.Connectors[0].Scopes, []string{"read", "write", "control"}) {
		t.Fatalf("connector.list = %+v", list)
	}

	rotated := e.refresh(t, first, first.token.RefreshToken, http.StatusOK)
	if rotated.RefreshToken == first.token.RefreshToken || rotated.AccessToken == first.token.AccessToken {
		t.Fatalf("refresh did not rotate: %+v", rotated)
	}
	if status, _, body := e.bearer(first.token.AccessToken); status != http.StatusUnauthorized {
		t.Fatalf("the access token refresh replaced = %d %s", status, body)
	}
	live := e.session(t, rotated.AccessToken)
	if err := e.admin.Call(protocol.MethodConnectorRevoke, protocol.ConnectorRevokeParams{ConnectorID: list.Connectors[0].ID}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := live.ListTools(ctx, nil); err == nil {
		t.Fatal("a revoked connector still lists tools")
	}

	second := e.connect(t, allScopes...)
	next := e.refresh(t, second, second.token.RefreshToken, http.StatusOK)
	if raced := e.refresh(t, second, second.token.RefreshToken, http.StatusBadRequest); raced.Error != "invalid_grant" ||
		!strings.Contains(raced.Description, "less than a minute ago") {
		t.Fatalf("refresh token rotated just now = %+v", raced)
	}
	if status, _, body := e.bearer(next.AccessToken); status != http.StatusOK {
		t.Fatalf("connector after a refresh token rotated just now was used again = %d %s, want kept", status, body)
	}
	e.skew.Store(int64(store.RefreshReuseGrace))
	if reused := e.refresh(t, second, second.token.RefreshToken, http.StatusBadRequest); !strings.Contains(reused.Description, "already used") {
		t.Fatalf("reused refresh token = %+v", reused)
	}
	if status, _, body := e.bearer(next.AccessToken); status != http.StatusUnauthorized {
		t.Fatalf("connector after a reused refresh token = %d %s, want revoked", status, body)
	}
	e.refresh(t, second, next.RefreshToken, http.StatusBadRequest)

	third := e.connect(t, allScopes...)
	if err := e.db.RevokeDevice(ctx, e.deviceID); err != nil {
		t.Fatal(err)
	}
	if status, _, body := e.bearer(third.token.AccessToken); status != http.StatusUnauthorized || !strings.Contains(body, "revoked") {
		t.Fatalf("connector of a revoked device = %d %s", status, body)
	}
	if refused := e.refresh(t, third, third.token.RefreshToken, http.StatusBadRequest); !strings.Contains(refused.Description, "revoked") {
		t.Fatalf("refresh on a revoked device = %+v", refused)
	}
}

// The consent page grants read always and write and control only when
// checked, tools/list shows only the granted tools, and a connector's
// write calls are limited per minute with the time to retry.
func TestConnectorScopes(t *testing.T) {
	e := newEnv(t)
	tools := func(c *connection) []string {
		var names []string
		for tool, err := range c.session.Tools(t.Context(), nil) {
			if err != nil {
				t.Fatal(err)
			}
			names = append(names, tool.Name)
		}
		slices.Sort(names)
		return names
	}
	reader := e.connect(t)
	if got, want := tools(reader), sorted(readTools); !slices.Equal(got, want) {
		t.Fatalf("tools with no box checked = %v, want %v", got, want)
	}
	for name, args := range map[string]map[string]any{
		"aether_launch_run":  {"workspace_id": e.ws.ID, "harness": "claude", "task": "t"},
		"aether_message_run": {"run_id": "run-1", "message": "m"},
		"aether_stop_run":    {"run_id": "run-1"},
		"aether_answer":      {"run_id": "run-1", "answer_id": "q-1", "answer": "a"},
	} {
		res, err := reader.session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err == nil && !res.IsError {
			t.Fatalf("%s by name without its scope = %+v", name, res)
		}
	}
	if got := e.runs.launches(); len(got) != 0 {
		t.Fatalf("launches without the write scope = %v", got)
	}
	writer := e.connect(t, "write", "admin")
	if got, want := tools(writer), sorted(readTools, writeTools); !slices.Equal(got, want) {
		t.Fatalf("tools with write checked = %v, want %v", got, want)
	}
	var list protocol.ConnectorListResult
	if err := e.admin.Call(protocol.MethodConnectorList, struct{}{}, &list); err != nil {
		t.Fatal(err)
	}
	scopes := make([]string, 0, len(list.Connectors))
	for _, c := range list.Connectors {
		scopes = append(scopes, strings.Join(c.Scopes, " "))
	}
	if slices.Sort(scopes); !slices.Equal(scopes, []string{"read", "read write"}) {
		t.Fatalf("connector.list = %+v", list)
	}

	launch := map[string]any{"workspace_id": e.ws.ID, "harness": "claude", "task": "t"}
	for range writeBurst {
		e.callTool(t, writer.session, "aether_launch_run", launch)
	}
	res, err := writer.session.CallTool(t.Context(), &mcp.CallToolParams{Name: "aether_launch_run", Arguments: launch})
	if err != nil {
		t.Fatal(err)
	}
	if text := fmt.Sprint(res.Content[0].(*mcp.TextContent).Text); !res.IsError ||
		!strings.Contains(text, fmt.Sprintf("used its %d write calls and gets one more every 2s; retry in 2s", writeBurst)) {
		t.Fatalf("write call over the limit = %v %s", res.IsError, text)
	}
	e.callTool(t, writer.session, "aether_list_runs", nil)
	e.skew.Store(int64(2 * time.Second))
	e.callTool(t, writer.session, "aether_launch_run", launch)
}

// A launch through the connector on an account shared with the member runs
// on that account and its receipt says so; the server refuses an account
// not shared with them, with its own error.
func TestConnectorLaunchOnSharedAccount(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	var info protocol.ServerInfoResult
	if err := e.admin.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil {
		t.Fatal(err)
	}
	grace := &domain.Member{DisplayName: "Grace", PublicKey: string(ssh.MarshalAuthorizedKey(newSigner(t).PublicKey())), Color: "#3cb44b",
		Role: domain.RoleCollaborator}
	bo := &domain.Member{DisplayName: "Bo", PublicKey: string(ssh.MarshalAuthorizedKey(newSigner(t).PublicKey())), Color: "#ffe119",
		Role: domain.RoleCollaborator}
	for _, m := range []*domain.Member{grace, bo} {
		if err := e.db.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.db.ShareAccount(ctx, grace.ID, domain.MemberID(info.Member.ID)); err != nil {
		t.Fatal(err)
	}
	writer := e.connect(t, "write")
	out := e.callTool(t, writer.session, "aether_launch_run", map[string]any{"workspace_id": e.ws.ID, "harness": "claude", "task": "t",
		"account_member_id": string(grace.ID), "no_fallback": true})
	var res struct {
		Launch struct {
			Account  map[string]string `json:"account"`
			Fallback map[string]string `json:"fallback"`
		} `json:"launch"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if got := e.runs.launches(); !slices.Equal(got, []string{string(e.ws.ID) + " claude t via Test client on " + string(grace.ID)}) {
		t.Fatalf("launches = %v", got)
	}
	if a := res.Launch.Account; a["member_id"] != string(grace.ID) || a["name"] != "Grace" || a["access"] != "shared" || res.Launch.Fallback["policy"] != "off" {
		t.Fatalf("receipt = %s", out)
	}
	r, err := writer.session.CallTool(ctx, &mcp.CallToolParams{Name: "aether_launch_run", Arguments: map[string]any{"workspace_id": e.ws.ID,
		"harness": "claude", "task": "t2", "account_member_id": string(bo.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	if text := r.Content[0].(*mcp.TextContent).Text; !r.IsError || !strings.Contains(text, "member "+string(bo.ID)+" has not shared their account with you") {
		t.Fatalf("launch on an unshared account = %v %s", r.IsError, text)
	}
}

// A connector granted an op's scope, and read, can make every call the op
// declares: the server admits each method under read or under that scope.
// Write and control are separate boxes, so a control op cannot lean on
// write.
func TestOpsMethodsFitTheirConnectorScope(t *testing.T) {
	for _, o := range ops.Ops {
		if o.Scope == "" {
			continue
		}
		for _, method := range o.Methods {
			scopes, ok := sshd.ConnectorMethodScopes(method)
			if !ok {
				t.Errorf("%s calls %s, which the server refuses to every connector", o.Name, method)
				continue
			}
			if !slices.Contains(scopes, sshd.ConnectorScopeRead) && !slices.Contains(scopes, string(o.Scope)) {
				t.Errorf("%s (scope %s) calls %s, which needs %v", o.Name, o.Scope, method, scopes)
			}
		}
	}
}

// connector.revoke ends a connector at once: its open session, its
// access token and its refresh token all fail on their next use.
func TestRevokeEndsEveryTokenAndOpenSession(t *testing.T) {
	e := newEnv(t)
	c := e.connect(t, allScopes...)
	e.callTool(t, c.session, "aether_launch_run", map[string]any{"workspace_id": e.ws.ID, "harness": "claude", "task": "t"})
	var list protocol.ConnectorListResult
	if err := e.admin.Call(protocol.MethodConnectorList, struct{}{}, &list); err != nil || len(list.Connectors) != 1 {
		t.Fatalf("connector.list = %+v %v", list, err)
	}
	id := list.Connectors[0].ID
	if err := e.admin.Call(protocol.MethodConnectorRevoke, protocol.ConnectorRevokeParams{ConnectorID: id}, nil); err != nil {
		t.Fatal(err)
	}

	if res, err := c.session.CallTool(t.Context(), &mcp.CallToolParams{Name: "aether_list_runs"}); err == nil && !res.IsError {
		t.Fatalf("the open session's call after revoke = %+v, want refused", res)
	}
	if status, _, body := e.bearer(c.token.AccessToken); status != http.StatusUnauthorized || !strings.Contains(body, "revoked") {
		t.Fatalf("access token after revoke = %d %s, want 401", status, body)
	}
	if refused := e.refresh(t, c, c.token.RefreshToken, http.StatusBadRequest); refused.Error != "invalid_grant" || !strings.Contains(refused.Description, "revoked") {
		t.Fatalf("refresh token after revoke = %+v", refused)
	}
	if got := e.runs.launches(); len(got) != 1 {
		t.Fatalf("launches = %v, want only the one before revoke", got)
	}

}

// Refreshes racing with one refresh token must rotate it once and keep
// the connector.
func TestConnectorExpiryAndConcurrentRefresh(t *testing.T) {
	e := newEnv(t)
	c := e.connect(t)
	e.skew.Store(int64(accessTokenTTL))
	status, header, body := e.bearer(c.token.AccessToken)
	if challenge := header.Get("WWW-Authenticate"); status != http.StatusUnauthorized || !strings.Contains(challenge, "resource_metadata=") ||
		!strings.Contains(body, "the access token expired at") {
		t.Fatalf("expired access token = %d %q %s, want 401 naming the metadata", status, challenge, body)
	}
	refreshed := e.refresh(t, c, c.token.RefreshToken, http.StatusOK)
	if status, _, body := e.bearer(refreshed.AccessToken); status != http.StatusOK {
		t.Fatalf("the refreshed access token = %d %s", status, body)
	}

	const racers = 8
	type reply struct {
		status int
		body   string
	}
	replies := make(chan reply, racers)
	var wg sync.WaitGroup
	for range racers {
		wg.Go(func() {
			resp, err := http.PostForm(e.base+"/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {c.clientID},
				"refresh_token": {refreshed.RefreshToken}, "resource": {e.base}})
			if err != nil {
				replies <- reply{body: err.Error()}
				return
			}
			body, _ := readBody(resp)
			replies <- reply{resp.StatusCode, body}
		})
	}
	wg.Wait()
	close(replies)
	var winners []tokenReply
	for r := range replies {
		switch r.status {
		case http.StatusOK:
			var won tokenReply
			if err := json.Unmarshal([]byte(r.body), &won); err != nil {
				t.Fatalf("the winning refresh = %s: %v", r.body, err)
			}
			winners = append(winners, won)
		case http.StatusBadRequest:
		default:
			t.Fatalf("a racing refresh = %d %s", r.status, r.body)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("%d of %d racing refreshes got tokens, want 1", len(winners), racers)
	}
	if status, _, body := e.bearer(winners[0].AccessToken); status != http.StatusOK {
		t.Fatalf("the winning access token = %d %s", status, body)
	}
	if next := e.refresh(t, c, winners[0].RefreshToken, http.StatusOK); next.AccessToken == "" {
		t.Fatal("the winning refresh token issued no access token")
	}
	var list protocol.ConnectorListResult
	if err := e.admin.Call(protocol.MethodConnectorList, struct{}{}, &list); err != nil || len(list.Connectors) != 1 {
		t.Fatalf("connector.list after racing refreshes = %+v %v, want the connector kept", list, err)
	}
}

// Wrong pairing codes lock out only the address that sent them, so no one
// can deny pairing to another member.
func TestWrongCodesLockOutOnlyTheirAddress(t *testing.T) {
	e := newEnv(t)
	post := e.consentPoster(t)
	for range wrongCodeLimit {
		if status, _ := post("[2001:db8::1]:4000", "zzzz-zzzz"); status != http.StatusForbidden {
			t.Fatalf("wrong code = %d", status)
		}
	}
	if status, body := post("[2001:db8::2]:4001", "zzzz-zzzz"); status != http.StatusTooManyRequests || !strings.Contains(body, "came from your address") {
		t.Fatalf("wrong code from the same /64 after the limit = %d %s", status, body)
	}
	if status, body := post("192.0.2.7:5000", e.mintCode(t)); status != http.StatusSeeOther {
		t.Fatalf("right code from another address = %d %s", status, body)
	}
}

// An address whose window of wrong codes has passed starts a new one,
// rather than guessing freely until the next sweep forgets it: here the
// sweep at 65s keeps the address's window from 10s.
func TestWrongCodeWindowRestarts(t *testing.T) {
	e := newEnv(t)
	post := e.consentPoster(t)
	post("192.0.2.1:4000", "zzzz-zzzz")
	e.skew.Store(int64(10 * time.Second))
	post("192.0.2.2:4000", "zzzz-zzzz")
	e.skew.Store(int64(65 * time.Second))
	post("192.0.2.3:4000", "zzzz-zzzz")
	e.skew.Store(int64(75 * time.Second))
	checked := 0
	for range 1000 {
		switch status, body := post("192.0.2.2:4000", "zzzz-zzzz"); status {
		case http.StatusForbidden:
			checked++
		case http.StatusTooManyRequests:
		default:
			t.Fatalf("wrong code = %d %s", status, body)
		}
	}
	if checked != wrongCodeLimit {
		t.Fatalf("checked %d of 1000 wrong codes from one address in a minute, want %d", checked, wrongCodeLimit)
	}
}

// Wrong codes from many addresses, each under its own limit, stop pairing
// server-wide at serverWrongCodeLimit a minute, and it resumes after.
func TestWrongCodesServerWideCeiling(t *testing.T) {
	e := newEnv(t)
	post := e.consentPoster(t)
	for i := range serverWrongCodeLimit {
		from := fmt.Sprintf("[2001:db8:0:%x::1]:4000", i/wrongCodeLimit)
		if status, body := post(from, "zzzz-zzzz"); status != http.StatusForbidden {
			t.Fatalf("wrong code %d from %s = %d %s", i, from, status, body)
		}
	}
	code := e.mintCode(t)
	if status, body := post("192.0.2.7:5000", code); status != http.StatusTooManyRequests ||
		!strings.Contains(body, fmt.Sprintf("%d wrong codes came to this server from all addresses", serverWrongCodeLimit)) {
		t.Fatalf("right code past the server-wide limit = %d %s", status, body)
	}
	e.skew.Store(int64(wrongCodeWindow))
	if status, body := post("192.0.2.7:5000", code); status != http.StatusSeeOther {
		t.Fatalf("right code a minute later = %d %s", status, body)
	}
}

// The write buckets of connectors idle for a minute are dropped.
func TestWriteBucketsOfIdleConnectorsDropped(t *testing.T) {
	h := &handler{writes: map[string]*rate.Limiter{}}
	now := time.Now()
	for range writeBurst {
		if err := h.allowWrite("con-a", now); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.allowWrite("con-b", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, kept := h.writes["con-a"]; kept || len(h.writes) != 1 {
		t.Fatalf("write buckets a minute later = %v", slices.Collect(maps.Keys(h.writes)))
	}
}

// A sign-in issued within ClockSkew of the handler's start, by the edge's
// clock, may have been issued before it by this one, so it is refused.
func TestAssertionsBeforeStartRefused(t *testing.T) {
	started := time.Now()
	h := &handler{started: started}
	for _, tt := range []struct {
		issued time.Time
		ok     bool
	}{
		{started.Add(-time.Second), false},
		{started, false},
		{started.Add(edgeproto.ClockSkew - time.Nanosecond), false},
		{started.Add(edgeproto.ClockSkew), true},
	} {
		if err := h.beforeStart(tt.issued); (err == nil) != tt.ok {
			t.Fatalf("beforeStart(started+%s) = %v, want ok %v", tt.issued.Sub(started), err, tt.ok)
		}
	}
}

// An edge before VersionConnectorSignIn passes a client's own
// Aether-Edge-Browser through, so the handler binds no sign-in to it.
func TestBrowserOnlyFromBindingEdge(t *testing.T) {
	srv := new(sshd.Server)
	edge := &edgeAt{}
	srv.SetEdgeOwner(edge)
	h := &handler{ssh: srv}
	r := httptest.NewRequest(http.MethodPost, "/mcp/authorize", nil)
	r.Header.Set(edgeproto.HeaderBrowser, "forged")
	edge.version.Store(edgeproto.VersionConnectorSignIn - 1)
	if got := h.edgeBrowser(r); got != "" {
		t.Fatalf("browser from a protocol %d edge = %q, want none", edge.version.Load(), got)
	}
	edge.version.Store(edgeproto.VersionConnectorSignIn)
	if got := h.edgeBrowser(r); got != "forged" {
		t.Fatalf("browser from a protocol %d edge = %q, want its header", edge.version.Load(), got)
	}
}

// consentPoster posts a consent form for one authorization request with a
// pairing code, as the edge relays it from a client address.
func (e *env) consentPoster(t *testing.T) func(from, code string) (int, string) {
	t.Helper()
	resp, err := http.Get(e.base + "/authorize?" + url.Values{"response_type": {"code"}, "client_id": {e.clientID},
		"redirect_uri": {e.redirect}, "code_challenge": {oauth2.S256ChallengeFromVerifier(oauth2.GenerateVerifier())},
		"code_challenge_method": {"S256"}, "resource": {e.base}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	page, _ := readBody(resp)
	m := requestField.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("consent page = %d %s", resp.StatusCode, page)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return func(from, code string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, e.base+"/authorize", strings.NewReader(url.Values{"request": {m[1]}, "code": {code}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set(edgeproto.HeaderClientAddr, from)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := readBody(resp)
		return resp.StatusCode, body
	}
}

type env struct {
	db       *store.DB
	runs     *fakeRuns
	ws       *domain.Workspace
	edge     *edgeAt
	origin   string
	base     string
	deviceID domain.DeviceID
	// clientID is the client_id metadata document the test serves, on
	// clientHost, which lists redirect.
	clientID, clientHost, redirect string
	// device calls as the member over their approved edge device key,
	// admin over their member SSH key.
	device, admin *protocol.Client
	// skew moves the handler's clock, frozen at the env's start, forward.
	skew *atomic.Int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()
	db, err := storetest.Open(filepath.Join(dir, "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bus, err := events.NewInProc(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	adminKey, deviceKey := newSigner(t), newSigner(t)
	member := &domain.Member{DisplayName: "Ada", PublicKey: string(ssh.MarshalAuthorizedKey(adminKey.PublicKey())), Color: "#e6194b", Role: domain.RoleAdmin}
	if err = db.CreateMember(ctx, member); err != nil {
		t.Fatal(err)
	}
	if err = db.BindIdentity(ctx, &domain.Identity{Member: member.ID, Provider: "github", Subject: "1", Login: "ada"}); err != nil {
		t.Fatal(err)
	}
	dev := &domain.Device{Member: member.ID, Provider: "github", Subject: "1", Login: "ada", Label: "laptop",
		Credential: edgeproto.DeviceKeyLine(deviceKey.PublicKey()), Status: domain.DeviceApproved, ApprovedBy: member.ID}
	if err = db.RegisterDevice(ctx, dev); err != nil {
		t.Fatal(err)
	}
	ws := &domain.Workspace{Name: "proj", BaseBranch: "main"}
	if err = db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}

	runs := &fakeRuns{}
	srv, err := sshd.New(sshd.Config{Addr: "127.0.0.1:0", HostKeyPath: filepath.Join(dir, "ssh", "host_ed25519_key"),
		Store: db, Bus: bus, Git: noGit{}, PTY: noPTY{}, Runs: runs, Name: "devbox", EdgeAccess: edgeproto.PolicyApprovedDevices})
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- srv.Serve(serveCtx) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
		<-served
	})
	for srv.Addr() == nil {
		time.Sleep(time.Millisecond)
	}

	clientLn := listen(t)
	clientHost := clientLn.Addr().String()
	clientID, redirect := "https://"+clientHost+"/client.json", "https://"+clientHost+"/callback"
	doc, err := json.Marshal(map[string]any{"client_id": clientID, "client_name": "Test client", "redirect_uris": []string{redirect}})
	if err != nil {
		t.Fatal(err)
	}
	clients := http.NewServeMux()
	clients.HandleFunc("GET /client.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(doc)
	})
	clientSrv := serve(t, clientLn, clients, true)

	ln := listen(t)
	origin := "http://" + ln.Addr().String()
	edge := &edgeAt{origin: origin}
	edge.version.Store(2)
	srv.SetEdgeOwner(edge)
	h, err := New(Config{BaseURL: srv.ConnectorURL(), Store: db, SSH: srv, KeyPath: filepath.Join(dir, "connector", "hmac.key"),
		ClientHosts: []string{clientHost}, Transport: clientSrv.Client().Transport})
	if err != nil {
		t.Fatal(err)
	}
	skew := new(atomic.Int64)
	start := time.Now()
	h.(*handler).now = func() time.Time { return start.Add(time.Duration(skew.Load())) }
	serve(t, ln, h, false)

	return &env{db: db, runs: runs, ws: ws, edge: edge, origin: origin, base: srv.ConnectorURL(), deviceID: dev.ID,
		clientID: clientID, clientHost: clientHost, redirect: redirect,
		device: dialControl(t, srv.Addr().String(), deviceKey), admin: dialControl(t, srv.Addr().String(), adminKey), skew: skew}
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

// serve serves h on ln, whose address the caller needed before h existed.
func serve(t *testing.T, ln net.Listener, h http.Handler, tls bool) *httptest.Server {
	t.Helper()
	ts := httptest.NewUnstartedServer(h)
	_ = ts.Listener.Close()
	ts.Listener = ln
	if tls {
		ts.StartTLS()
	} else {
		ts.Start()
	}
	t.Cleanup(ts.Close)
	return ts
}

// connection is one connector a client authorized.
type connection struct {
	session  *mcp.ClientSession
	token    *oauth2.Token
	clientID string
}

// The connector's tools by scope.
var (
	readTools = []string{"aether_list_workspaces", "aether_list_runs", "aether_get_run", "aether_run_history",
		"aether_needs_attention", "aether_activity_summary", "aether_get_swarm", "aether_run_changes",
		"aether_pr_status", "aether_overlaps", "aether_memory_search", "aether_integration", "search", "fetch", "aether_launch_options", "aether_changes"}
	writeTools = []string{"aether_launch_run", "aether_launch_swarm", "aether_message_run", "aether_answer", "aether_pause_run",
		"aether_resume_run", "aether_mark_reviewed", "aether_run_lifecycle", "aether_memory_add", "aether_memory_pin"}
	controlTools = []string{"aether_decide_approval", "aether_stop_run", "aether_cancel_swarm", "aether_handoff_run",
		"aether_replace_integrator", "aether_integration_decide", "aether_pr_create", "aether_pr_merge", "aether_swarm_manage", "aether_switch_run_mode"}
)

func sorted(lists ...[]string) []string {
	out := slices.Concat(lists...)
	slices.Sort(out)
	return out
}

// allScopes checks both boxes on the consent page.
var allScopes = []string{"write", "control"}

// connect mints a pairing code on the member's device and lets go-sdk's
// client discover and authorize with the test's client_id metadata
// document, pasting a wrong code first and checking the boxes of scopes.
func (e *env) connect(t *testing.T, scopes ...string) *connection {
	t.Helper()
	code := e.mintCode(t)
	c := &connection{clientID: e.clientID}
	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		ClientIDMetadataDocumentConfig: &auth.ClientIDMetadataDocumentConfig{URL: e.clientID},
		RedirectURL:                    e.redirect,
		AuthorizationCodeFetcher: func(_ context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			return e.consent(args.URL, code, scopes)
		},
		RequestRefreshToken: true,
		NewTokenSource: func(_ context.Context, _ *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
			c.token = token
			return oauth2.StaticTokenSource(token), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	c.session, err = client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: e.base, OAuthHandler: handler, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.session.Close() })
	return c
}

// mintCode mints a pairing code on the member's device, as aether mcp does.
func (e *env) mintCode(t *testing.T) string {
	t.Helper()
	var code protocol.ConnectorCodeResult
	if err := e.device.Call(protocol.MethodConnectorCode, struct{}{}, &code); err != nil {
		t.Fatal(err)
	}
	if code.URL != e.base {
		t.Fatalf("connector.code url = %s, want %s", code.URL, e.base)
	}
	return code.Code
}

var requestField = regexp.MustCompile(`name="request" value="([^"]+)"`)

// consent is the member in the browser: it opens the consent page, pastes
// a wrong code, then the right one with the boxes of scopes checked, and
// returns where it was sent.
func (e *env) consent(authURL, code string, scopes []string) (*auth.AuthorizationResult, error) {
	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := browser.Get(authURL)
	if err != nil {
		return nil, err
	}
	page, err := readBody(resp)
	if err != nil {
		return nil, err
	}
	m := requestField.FindStringSubmatch(page)
	if resp.StatusCode != http.StatusOK || m == nil || !strings.Contains(page, "Connect <strong>Test client ("+e.clientHost+")</strong> to <strong>devbox</strong>") ||
		!strings.Contains(page, `value="write" checked>`) || !strings.Contains(page, `value="control">`) {
		return nil, fmt.Errorf("consent page = %d %s", resp.StatusCode, page)
	}
	if resp, err = browser.PostForm(authURL, url.Values{"request": {m[1]}, "code": {"zzzz-zzzz"}}); err != nil {
		return nil, err
	}
	if page, err = readBody(resp); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(page, "wrong, expired or already used") {
		return nil, fmt.Errorf("wrong code = %d %s", resp.StatusCode, page)
	}
	if resp, err = browser.PostForm(authURL, url.Values{"request": {m[1]}, "code": {code}, "scope": scopes}); err != nil {
		return nil, err
	}
	if _, err = readBody(resp); err != nil {
		return nil, err
	}
	loc, err := resp.Location()
	if err != nil || resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc.String(), e.redirect+"?") {
		return nil, fmt.Errorf("right code = %d %v %v", resp.StatusCode, loc, err)
	}
	q := loc.Query()
	return &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}, nil
}

func readBody(resp *http.Response) (string, error) {
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

type tokenReply struct {
	tokenResponse
	tokenError
}

func (e *env) refresh(t *testing.T, c *connection, refreshToken string, want int) tokenReply {
	t.Helper()
	return e.tokenRequest(t, url.Values{"grant_type": {"refresh_token"}, "client_id": {c.clientID},
		"refresh_token": {refreshToken}, "resource": {e.base}}, want)
}

func (e *env) tokenRequest(t *testing.T, form url.Values, want int) tokenReply {
	t.Helper()
	resp, err := http.PostForm(e.base+"/token", form)
	if err != nil {
		t.Fatal(err)
	}
	body, err := readBody(resp)
	if err != nil {
		t.Fatal(err)
	}
	var out tokenReply
	if err := json.Unmarshal([]byte(body), &out); err != nil || resp.StatusCode != want {
		t.Fatalf("token %s = %d %s, want %d", form.Get("grant_type"), resp.StatusCode, body, want)
	}
	return out
}

// bearer posts tools/list with token and returns the answer.
func (e *env) bearer(token string) (int, http.Header, string) {
	req, _ := http.NewRequest(http.MethodPost, e.base, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err.Error()
	}
	body, _ := readBody(resp)
	return resp.StatusCode, resp.Header, body
}

// session connects with token alone, as a client holding it would.
func (e *env) session(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	httpClient := &http.Client{Transport: &oauth2.Transport{Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})}}
	cs, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: e.base, HTTPClient: httpClient, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (e *env) callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(res.StructuredContent)
	if res.IsError {
		t.Fatalf("%s failed: %+v", name, res.Content)
	}
	return string(out)
}

func getJSON(t *testing.T, u string, out any) {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d %v", u, resp.StatusCode, err)
	}
}

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// dialControl opens the control channel as signer, as the CLI does.
func dialControl(t *testing.T, addr string, signer ssh.Signer) *protocol.Client {
	t.Helper()
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{User: "aether", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.RequestSubsystem(protocol.SubsystemControl); err != nil {
		t.Fatal(err)
	}
	return protocol.NewClient(struct {
		io.Reader
		io.WriteCloser
	}{stdout, stdin})
}

type (
	noGit struct{ sshd.GitTransport }
	noPTY struct{ sshd.PTYAttacher }
)

// edgeAt is an edge at origin speaking version, as far as connectors go.
type edgeAt struct {
	sshd.EdgeOwner
	origin  string
	version atomic.Int64
}

func (e *edgeAt) Origin() string   { return e.origin }
func (e *edgeAt) EdgeVersion() int { return int(e.version.Load()) }

// fakeRuns records launches, with the connector client each came via, and
// answers the reads a run snapshot makes.
type fakeRuns struct {
	sshd.RunController
	mu       sync.Mutex
	launched []string
}

func (f *fakeRuns) Launch(ctx context.Context, ws domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode) (*domain.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	launch := fmt.Sprintf("%s %s %s via %s", ws, harness, task, domain.ViaFrom(ctx))
	if account != member {
		launch += " on " + string(account)
	}
	f.launched = append(f.launched, launch)
	return &domain.Run{ID: "run-launched", WorkspaceID: ws, MemberID: member, AccountMemberID: account, Task: task, Harness: harness, Mode: mode,
		Status: domain.RunQueued}, nil
}

func (f *fakeRuns) LaunchWithOptions(ctx context.Context, ws domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode,
	opts domain.LaunchOptions) (*domain.Run, error) {
	r, err := f.Launch(ctx, ws, member, account, task, harness, mode)
	if err == nil {
		r.Fallback = opts.Fallback
	}
	return r, err
}

func (*fakeRuns) SwarmRefusal(string, domain.LaunchMode) error { return nil }

func (*fakeRuns) AgentSwitchable(context.Context, domain.MemberID, domain.MemberID, string) (bool, error) {
	return false, nil
}

func (f *fakeRuns) launches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.launched)
}

func (*fakeRuns) Paused(domain.RunID) bool { return false }
func (*fakeRuns) DefaultMode(context.Context, domain.MemberID, domain.MemberID, string) (domain.LaunchMode, error) {
	return domain.LaunchACP, nil
}
func (*fakeRuns) PendingInputs(domain.RunID) []domain.RunInputRequest          { return nil }
func (*fakeRuns) Cause(domain.RunID, domain.RunActivity) events.RunStatusCause { return "" }
func (*fakeRuns) Liveness(domain.RunID) (domain.RunLiveness, bool) {
	return domain.RunLiveness{}, false
}
func (*fakeRuns) Switching(domain.RunID) domain.LaunchMode { return "" }
func (*fakeRuns) Retention(*domain.Run) (scheduler.RetentionInfo, error) {
	return scheduler.RetentionInfo{}, nil
}
