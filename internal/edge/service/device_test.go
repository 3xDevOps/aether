package edge

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

func startDevice(t *testing.T, h *harness) edgeproto.DeviceStartResponse {
	t.Helper()
	var start edgeproto.DeviceStartResponse
	status, msg := h.apiCall(t, http.MethodPost, edgeproto.PathDeviceStart, "",
		edgeproto.DeviceStartRequest{Label: "laptop", Key: newDeviceKey(t)}, &start)
	if status != http.StatusOK {
		t.Fatalf("device start: %d %s", status, msg)
	}
	return start
}

func pollDevice(t *testing.T, h *harness, deviceCode string) (edgeproto.DeviceTokenResponse, int, string) {
	t.Helper()
	var tok edgeproto.DeviceTokenResponse
	status, msg := h.apiCall(t, http.MethodPost, edgeproto.PathDeviceToken, "",
		edgeproto.DeviceTokenRequest{DeviceCode: deviceCode}, &tok)
	return tok, status, msg
}

func signedInBrowser(t *testing.T, h *harness) *browser {
	t.Helper()
	h.setGitHubUser(1, "owner", "owner@example.test", true)
	b := h.browser(t)
	b.signIn(t, edgeproto.ProviderGitHub)
	return b
}

func TestDeviceFlow(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	start := startDevice(t, h)
	if start.VerificationURI != testSignin+"/device" || strings.Contains(start.VerificationURI, start.UserCode) {
		t.Errorf("verification URI = %q, want the edge's /device with no code in it", start.VerificationURI)
	}

	if _, status, msg := pollDevice(t, h, start.DeviceCode); status != http.StatusBadRequest || msg != edgeproto.DevicePending {
		t.Fatalf("first poll = %d %q, want 400 %s", status, msg, edgeproto.DevicePending)
	}
	if _, _, msg := pollDevice(t, h, start.DeviceCode); msg != edgeproto.DeviceSlowDown {
		t.Errorf("immediate second poll = %q, want %s", msg, edgeproto.DeviceSlowDown)
	}

	resp, page := b.post(t, "/device", url.Values{"user_code": {strings.ToLower(start.UserCode)}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "laptop") || !strings.Contains(page, "SHA256:") ||
		!strings.Contains(page, "Confirm only if you started this sign-in yourself") ||
		!strings.Contains(page, "<dt>Requested from</dt><dd><code>127.0.0.1</code> (the address of this browser)") {
		t.Fatalf("confirm page: %s\n%s", resp.Status, page)
	}
	resp, page = b.post(t, "/device/confirm", url.Values{"user_code": {start.UserCode}, "decision": {"approve"}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "is signed in") {
		t.Fatalf("approve: %s\n%s", resp.Status, page)
	}

	h.clock.Advance(pollInterval)
	tok, status, msg := pollDevice(t, h, start.DeviceCode)
	if status != http.StatusOK {
		t.Fatalf("poll after approval = %d %s", status, msg)
	}
	if tok.Account.Login != "owner" || tok.Device.Label != "laptop" || !edgeproto.ValidToken(tok.Token) {
		t.Errorf("token response = %+v", tok)
	}

	var servers edgeproto.ServersResponse
	if status, msg := h.apiCall(t, http.MethodGet, edgeproto.PathServers, tok.Token, nil, &servers); status != http.StatusOK {
		t.Fatalf("servers with the new token = %d %s", status, msg)
	}

	// The device code is spent: polling again yields no second token.
	h.clock.Advance(pollInterval)
	if _, status, msg := pollDevice(t, h, start.DeviceCode); status != http.StatusBadRequest || msg != edgeproto.DeviceExpired {
		t.Errorf("poll after redemption = %d %q, want 400 %s", status, msg, edgeproto.DeviceExpired)
	}
	// And its user code cannot be confirmed again.
	resp, _ = b.post(t, "/device/confirm", url.Values{"user_code": {start.UserCode}, "decision": {"approve"}})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("confirm a redeemed code: %s, want 404", resp.Status)
	}
}

func TestDeviceFlowDenied(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	start := startDevice(t, h)
	resp, _ := b.post(t, "/device/confirm", url.Values{"user_code": {start.UserCode}, "decision": {"deny"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deny: %s", resp.Status)
	}
	if _, status, msg := pollDevice(t, h, start.DeviceCode); status != http.StatusBadRequest || msg != edgeproto.DeviceDenied {
		t.Fatalf("poll after denial = %d %q, want 400 %s", status, msg, edgeproto.DeviceDenied)
	}
	h.clock.Advance(pollInterval)
	if _, _, msg := pollDevice(t, h, start.DeviceCode); msg != edgeproto.DeviceExpired {
		t.Errorf("poll after the denial was reported = %q, want %s", msg, edgeproto.DeviceExpired)
	}
}

func TestDeviceFlowExpired(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	start := startDevice(t, h)
	h.clock.Advance(deviceCodeTTL)
	resp, page := b.post(t, "/device", url.Values{"user_code": {start.UserCode}})
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(page, "no sign-in is waiting") {
		t.Errorf("enter an expired code: %s\n%s", resp.Status, page)
	}
	resp, _ = b.post(t, "/device/confirm", url.Values{"user_code": {start.UserCode}, "decision": {"approve"}})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("approve an expired code: %s, want 404", resp.Status)
	}
	if _, _, msg := pollDevice(t, h, start.DeviceCode); msg != edgeproto.DeviceExpired {
		t.Errorf("poll an expired code = %q, want %s", msg, edgeproto.DeviceExpired)
	}
}

func TestDeviceStartRefusesBadKeys(t *testing.T) {
	h := newHarness(t)
	for _, key := range []string{"", "ssh-rsa AAAAB3NzaC1yc2E=", `command="sh" ` + newDeviceKey(t)} {
		status, _ := h.apiCall(t, http.MethodPost, edgeproto.PathDeviceStart, "",
			edgeproto.DeviceStartRequest{Label: "laptop", Key: key}, nil)
		if status != http.StatusBadRequest {
			t.Errorf("device start with key %q = %d, want 400", key, status)
		}
	}
}

func TestUserCodeEntryIsRateLimited(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	var last *http.Response
	for range 11 {
		last, _ = b.post(t, "/device", url.Values{"user_code": {"BCDF-GHJK"}})
	}
	if last.StatusCode != http.StatusTooManyRequests {
		t.Errorf("11th code entry: %s, want 429", last.Status)
	}
}

func TestUserCodeEntryIsLimitedPerAccount(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	for range 10 {
		b.post(t, "/device", url.Values{"user_code": {"BCDF-GHJK"}})
	}
	// The address block has earned five entries back; the account has
	// not earned one.
	h.clock.Advance(30 * time.Second)
	if resp, _ := b.post(t, "/device", url.Values{"user_code": {"BCDF-GHJK"}}); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("code entry over the account's limit: %s, want 429", resp.Status)
	}
}

func TestDeviceTokenPollingIsRateLimited(t *testing.T) {
	h := newHarness(t)
	code := edgeproto.NewToken()
	for range 30 {
		if _, status, msg := pollDevice(t, h, code); status != http.StatusBadRequest {
			t.Fatalf("poll within the limit = %d %q", status, msg)
		}
	}
	if _, status, msg := pollDevice(t, h, code); status != http.StatusTooManyRequests || msg != string(edgeproto.RefusalTooMany) {
		t.Fatalf("poll over the limit = %d %q, want 429", status, msg)
	}
}

func TestRevokedTokenIsRefused(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	issue := func() edgeproto.DeviceTokenResponse {
		start := startDevice(t, h)
		b.post(t, "/device/confirm", url.Values{"user_code": {start.UserCode}, "decision": {"approve"}})
		tok, status, msg := pollDevice(t, h, start.DeviceCode)
		if status != http.StatusOK {
			t.Fatalf("poll = %d %s", status, msg)
		}
		return tok
	}

	byLogout := issue()
	if status, msg := h.apiCall(t, http.MethodPost, edgeproto.PathLogout, byLogout.Token, nil, nil); status != http.StatusNoContent {
		t.Fatalf("logout = %d %s", status, msg)
	}
	byPage := issue()
	if resp, body := b.post(t, "/devices/revoke", url.Values{"device": {byPage.Device.ID}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("revoke on the Devices page: %s\n%s", resp.Status, body)
	}

	for _, tok := range []edgeproto.DeviceTokenResponse{byLogout, byPage} {
		status, msg := h.apiCall(t, http.MethodGet, edgeproto.PathServers, tok.Token, nil, nil)
		if status != http.StatusUnauthorized || msg != string(edgeproto.RefusalTokenRevoked) {
			t.Errorf("servers with a revoked token = %d %q", status, msg)
		}
		if !slices.Contains(h.link.revoked, tok.Device.ID) {
			t.Errorf("relay was not told device %s is revoked", tok.Device.ID)
		}
	}
	if status, msg := h.apiCall(t, http.MethodGet, edgeproto.PathServers, "", nil, nil); status != http.StatusUnauthorized ||
		msg != string(edgeproto.RefusalTokenRequired) {
		t.Errorf("servers without a token = %d %q", status, msg)
	}

	// Another account cannot revoke this account's device.
	h.setGitHubUser(2, "other", "other@example.test", true)
	other := h.browser(t)
	other.signIn(t, edgeproto.ProviderGitHub)
	kept := issue()
	if resp, _ := other.post(t, "/devices/revoke", url.Values{"device": {kept.Device.ID}}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("revoke another account's device: %s, want 404", resp.Status)
	}
}

func TestAPIRequiresVersion(t *testing.T) {
	h := newHarness(t)
	req, err := http.NewRequest(http.MethodGet, testRelay+edgeproto.PathEdgeInfo, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() //nolint:errcheck // test
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("API call without %s: %s", edgeproto.HeaderVersion, resp.Status)
	}
	req.Header.Set(edgeproto.HeaderVersion, "0")
	resp, err = h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() //nolint:errcheck // test
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("API call with version 0: %s", resp.Status)
	}
	var info edgeproto.EdgeInfo
	if status, _ := h.apiCallOn(t, testRelay, http.MethodGet, edgeproto.PathEdgeInfo, "", nil, &info); status != http.StatusOK ||
		info.Validate() != nil || !info.Key.Equal(h.svc.EdgeKey()) || info.SigninOrigin != testSignin ||
		info.Version != edgeproto.Version || info.MinVersion != edgeproto.MinVersion {
		t.Errorf("edge info = %d %+v", status, info)
	}
}

func TestServersList(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	tok := approvedDevice(t, h, b)
	if tok.Account.Validate() != nil || tok.Account.Login != "owner" {
		t.Fatalf("device token names account %+v", tok.Account)
	}
	id := testServerID(t)
	h.claim(t, id, "workstation", tok.Account.Account)
	h.link.servers = map[string]string{id: "workstation"}
	var servers edgeproto.ServersResponse
	h.apiCall(t, http.MethodGet, edgeproto.PathServers, tok.Token, nil, &servers)
	want := edgeproto.ServerInfo{ID: id, Name: "workstation", Online: true, Role: "admin",
		AccessPolicy: edgeproto.PolicyAccount, Kind: edgeproto.ServerSelfHosted}
	if len(servers.Servers) != 1 || servers.Servers[0] != want {
		t.Errorf("servers = %+v, want [%+v]", servers.Servers, want)
	}
	// A later enrollment announces another policy.
	if _, err := h.svc.ServerConnected(context.Background(), id, "renamed", edgeproto.PolicyApprovedDevices); err != nil {
		t.Fatal(err)
	}
	h.apiCall(t, http.MethodGet, edgeproto.PathServers, tok.Token, nil, &servers)
	if got := servers.Servers[0]; got.AccessPolicy != edgeproto.PolicyApprovedDevices || got.Name != "renamed" {
		t.Errorf("servers after a new hello = %+v", got)
	}
	if _, page := b.get(t, "/servers"); !strings.Contains(page, "approved devices only") || !strings.Contains(page, "its owner's hardware") {
		t.Errorf("servers page lacks the policy and kind:\n%s", page)
	}
}

// TestSecretsAreStoredHashed runs every flow that mints a bearer secret and
// checks that none of them appears in the database files.
func TestSecretsAreStoredHashed(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	start := startDevice(t, h)
	b.post(t, "/device/confirm", url.Values{"user_code": {start.UserCode}, "decision": {"approve"}})
	tok, status, msg := pollDevice(t, h, start.DeviceCode)
	if status != http.StatusOK {
		t.Fatalf("poll = %d %s", status, msg)
	}
	var session string
	for _, c := range b.client.Jar.Cookies(mustParse(t, testSignin)) {
		if c.Name == sessionCookie {
			session = c.Value
		}
	}

	files, err := filepath.Glob(filepath.Join(h.dataDir, "edge.db*"))
	if err != nil || len(files) == 0 {
		t.Fatalf("database files: %v %v", files, err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for name, secret := range map[string]string{
			"device token": tok.Token, "device code": start.DeviceCode, "session": session,
			"user code": strings.ReplaceAll(start.UserCode, "-", ""),
		} {
			if secret == "" || bytes.Contains(data, []byte(secret)) {
				t.Errorf("%s %q is stored in %s", name, secret, filepath.Base(f))
			}
		}
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestPagesRender(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	start := startDevice(t, h)
	b.post(t, "/device/confirm", url.Values{"user_code": {start.UserCode}, "decision": {"approve"}})
	pollDevice(t, h, start.DeviceCode)
	for path, want := range map[string]string{
		"/device":   "aether login",
		"/servers":  "No servers yet",
		"/devices":  "laptop",
		"/account":  "Delete this account",
		"/edge.css": "--fg",
	} {
		resp, page := b.get(t, path)
		if resp.StatusCode != http.StatusOK || !strings.Contains(page, want) {
			t.Errorf("GET %s: %s, want a page containing %q\n%s", path, resp.Status, want, page)
		}
	}
	if resp, _ := h.browser(t).get(t, "/devices"); resp.StatusCode != http.StatusSeeOther ||
		resp.Header.Get("Location") != "/signin?next=%2Fdevices" {
		t.Errorf("signed-out /devices: %s to %q", resp.Status, resp.Header.Get("Location"))
	}
}
