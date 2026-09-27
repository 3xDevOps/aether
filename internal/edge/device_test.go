package edge

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/edgeproto"
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
	if start.VerificationURI != h.ts.URL+"/device" || strings.Contains(start.VerificationURI, start.UserCode) {
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
		!strings.Contains(page, "Confirm only if you started this sign-in yourself") {
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
	req, err := http.NewRequest(http.MethodGet, h.ts.URL+edgeproto.PathEdgeKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() //nolint:errcheck // test
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("API call without %s: %s", edgeproto.HeaderVersion, resp.Status)
	}
	req.Header.Set(edgeproto.HeaderVersion, "0")
	resp, err = h.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() //nolint:errcheck // test
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("API call with version 0: %s", resp.Status)
	}
	var key edgeproto.EdgeKeyResponse
	if status, _ := h.apiCall(t, http.MethodGet, edgeproto.PathEdgeKey, "", nil, &key); status != http.StatusOK ||
		!key.Key.Equal(h.svc.EdgeKey()) || key.Fingerprint != edgeproto.EdgeKeyFingerprint(h.svc.EdgeKey()) {
		t.Errorf("edge key = %d %+v", status, key)
	}
}

func TestClaimAPI(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	start := startDevice(t, h)
	b.post(t, "/device/confirm", url.Values{"user_code": {start.UserCode}, "decision": {"approve"}})
	tok, status, msg := pollDevice(t, h, start.DeviceCode)
	if status != http.StatusOK {
		t.Fatalf("poll = %d %s", status, msg)
	}
	id := testServerID(t)
	code, err := edgeproto.NewClaimCode(id)
	if err != nil {
		t.Fatal(err)
	}
	h.link.servers = map[string]string{id: "workstation"}
	h.link.claim = func(_ string, _ edgeproto.Account, d edgeproto.Device) error {
		if d.ID != tok.Device.ID || d.Key != tok.Device.Key {
			return errors.New("claim does not name the claiming device")
		}
		return nil
	}
	var res edgeproto.ClaimResponse
	if status, msg := h.apiCall(t, http.MethodPost, edgeproto.PathClaim, tok.Token,
		edgeproto.ClaimRequest{Code: code}, &res); status != http.StatusOK || res.ServerID != id || res.Name != "workstation" {
		t.Fatalf("claim = %d %q %+v", status, msg, res)
	}
	var servers edgeproto.ServersResponse
	h.apiCall(t, http.MethodGet, edgeproto.PathServers, tok.Token, nil, &servers)
	want := edgeproto.ServerInfo{ID: id, Name: "workstation", Online: true, Role: "admin"}
	if len(servers.Servers) != 1 || servers.Servers[0] != want {
		t.Errorf("servers = %+v, want [%+v]", servers.Servers, want)
	}
	if status, _ := h.apiCall(t, http.MethodPost, edgeproto.PathClaim, "", edgeproto.ClaimRequest{Code: code}, nil); status != http.StatusUnauthorized {
		t.Errorf("claim without a token = %d, want 401", status)
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
	id := testServerID(t)
	if err := h.svc.RecordClaim(context.Background(), id, "srv", sessionAccount(t, h, b)); err != nil {
		t.Fatal(err)
	}
	webCode := webSignIn(t, b, id, edgeproto.PKCEChallenge(edgeproto.NewVerifier()), false).Query().Get("code")
	var session string
	for _, c := range b.client.Jar.Cookies(mustParse(t, h.ts.URL)) {
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
			"device token": tok.Token, "device code": start.DeviceCode, "session": session, "web code": webCode,
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
		"/device":      "aether login",
		"/servers":     "No servers yet",
		"/servers/add": "Claim code",
		"/devices":     "laptop",
		"/edge.css":    "--fg",
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
