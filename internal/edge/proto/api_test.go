package edgeproto

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestPathPatterns(t *testing.T) {
	mux := http.NewServeMux()
	var got string
	mux.HandleFunc("GET "+PathConnect, func(_ http.ResponseWriter, r *http.Request) { got = "connect " + r.PathValue("server_id") })
	mux.HandleFunc("GET "+PathClaimConnect, func(_ http.ResponseWriter, r *http.Request) { got = "claim " + r.PathValue("server_id") })
	mux.HandleFunc("GET "+PathServerData, func(_ http.ResponseWriter, r *http.Request) { got = "data " + r.PathValue("conn_id") })

	id, conn := "wqc4lsjvzdzrwq3k5dabdtajwj", NewConnID()
	for path, want := range map[string]string{
		ConnectPath(id):      "connect " + id,
		ClaimConnectPath(id): "claim " + id,
		DataPath(conn):       "data " + conn,
	} {
		got = ""
		mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		if got != want {
			t.Errorf("%s routed to %q, want %q", path, got, want)
		}
	}
}

func TestRefusalStatus(t *testing.T) {
	tests := []struct {
		r    Refusal
		msg  string
		code int
	}{
		{RefusalTokenRevoked, "device token revoked", http.StatusUnauthorized},
		{RefusalNotMember, "not a member of this server", http.StatusForbidden},
		{RefusalUnknownServer, "unknown server", http.StatusNotFound},
		{RefusalNotConnected, "server is not connected to the edge", http.StatusServiceUnavailable},
		{RefusalNotAttached, "server did not attach", http.StatusGatewayTimeout},
		{RefusalTokenRequired, "device token required", http.StatusUnauthorized},
		{RefusalTooMany, "too many attempts", http.StatusTooManyRequests},
		{RefusalConnLimit, "connection limit reached", http.StatusTooManyRequests},
		{RefusalClaimWrong, "claim code is wrong", http.StatusForbidden},
		{RefusalClaimExpired, "claim code expired", http.StatusForbidden},
		{RefusalClaimExhausted, "claim code has no attempts left", http.StatusForbidden},
		{RefusalClaimed, "server is already claimed", http.StatusConflict},
		{RefusalServerBlocked, "server is blocked by this edge's operator", http.StatusForbidden},
		{RefusalAccountBlocked, "account is blocked by this edge's operator", http.StatusForbidden},
		{Refusal("device is pending approval"), "device is pending approval", http.StatusForbidden},
	}
	for _, tt := range tests {
		if tt.r.Error() != tt.msg || tt.r.Status() != tt.code {
			t.Errorf("%q: Error() = %q, Status() = %d; want %q, %d", tt.r, tt.r.Error(), tt.r.Status(), tt.msg, tt.code)
		}
	}
}

func TestDeviceStartRequestValidate(t *testing.T) {
	key := DeviceKeyLine(seedSigner(t, 1).PublicKey())
	tests := []struct {
		name string
		r    DeviceStartRequest
		ok   bool
	}{
		{"valid", DeviceStartRequest{Label: "laptop", Key: key}, true},
		{"no label", DeviceStartRequest{Key: key}, false},
		{"escape in label", DeviceStartRequest{Label: "\x1b[31mlaptop", Key: key}, false},
		{"long label", DeviceStartRequest{Label: strings.Repeat("l", maxIDText+1), Key: key}, false},
		{"no key", DeviceStartRequest{Label: "laptop"}, false},
	}
	for _, tt := range tests {
		if err := tt.r.Validate(); (err == nil) != tt.ok {
			t.Errorf("%s: Validate() = %v, want ok %v", tt.name, err, tt.ok)
		}
	}
}

func TestParseServerKind(t *testing.T) {
	for in, want := range map[string]ServerKind{
		"":            ServerSelfHosted,
		"self-hosted": ServerSelfHosted,
		"hosted":      ServerHosted,
	} {
		if got, err := ParseServerKind(in); err != nil || got != want {
			t.Errorf("ParseServerKind(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"Hosted", "self_hosted", "managed", " hosted"} {
		if got, err := ParseServerKind(in); err == nil {
			t.Errorf("ParseServerKind(%q) = %q, want an error", in, got)
		}
	}
}

// An edge that predates policies and kinds sends neither; the list still
// decodes, and each field reads as its default.
func TestServerInfoWithoutPolicyOrKind(t *testing.T) {
	var resp ServersResponse
	if err := json.Unmarshal([]byte(`{"servers":[{"id":"wqc4lsjvzdzrwq3k5dabdtajwj","name":"devbox","online":true,"role":"admin"}]}`), &resp); err != nil {
		t.Fatal(err)
	}
	s := resp.Servers[0]
	if p, err := ParseAccessPolicy(string(s.AccessPolicy)); err != nil || p != PolicyApprovedDevices {
		t.Fatalf("policy = %q, %v", p, err)
	}
	if k, err := ParseServerKind(string(s.Kind)); err != nil || k != ServerSelfHosted {
		t.Fatalf("kind = %q, %v", k, err)
	}
}

// The account id sits beside the provider identity in one JSON object.
func TestDeviceTokenResponseAccount(t *testing.T) {
	id := NewAccountID()
	resp := DeviceTokenResponse{Account: AccountInfo{ID: id, Account: Account{Provider: ProviderGitHub, Subject: "1001", Login: "octo-fake"}}}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Account map[string]any `json:"account"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Account["id"] != id || raw.Account["provider"] != ProviderGitHub || raw.Account["subject"] != "1001" {
		t.Fatalf("account encodes as %s", data)
	}
	var back DeviceTokenResponse
	if err := json.Unmarshal(data, &back); err != nil || back.Account != resp.Account || back.Account.Validate() != nil {
		t.Fatalf("round trip = %+v, %v", back.Account, err)
	}
}

func TestEdgeInfoValidate(t *testing.T) {
	key := edgeKey(1).Public().(ed25519.PublicKey)
	valid := EdgeInfo{SigninOrigin: "https://auth.example", Key: key, Fingerprint: EdgeKeyFingerprint(key), Version: Version, MinVersion: MinVersion}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*EdgeInfo)
	}{
		{"no sign-in origin", func(i *EdgeInfo) { i.SigninOrigin = "" }},
		{"sign-in origin with a path", func(i *EdgeInfo) { i.SigninOrigin = "https://auth.example/login" }},
		{"plain http sign-in origin", func(i *EdgeInfo) { i.SigninOrigin = "http://auth.example" }},
		{"short key", func(i *EdgeInfo) { i.Key = key[:16] }},
		{"fingerprint of another key", func(i *EdgeInfo) { i.Fingerprint = EdgeKeyFingerprint(edgeKey(2).Public().(ed25519.PublicKey)) }},
		{"no fingerprint", func(i *EdgeInfo) { i.Fingerprint = "" }},
		{"no versions", func(i *EdgeInfo) { i.Version, i.MinVersion = 0, 0 }},
		{"minimum above version", func(i *EdgeInfo) { i.MinVersion = i.Version + 1 }},
	}
	for _, tt := range tests {
		info := valid
		tt.mutate(&info)
		if err := info.Validate(); err == nil {
			t.Errorf("%s: Validate accepted it", tt.name)
		}
	}
}

func TestNewVerifier(t *testing.T) {
	if v := NewVerifier(); !ValidToken(v) {
		t.Fatalf("NewVerifier = %q", v)
	}
}

func TestRateLimitKey(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{"192.0.2.7", "192.0.2.7/32"},
		{"::ffff:192.0.2.7", "192.0.2.7/32"},
		{"2001:db8:1:2:3:4:5:6", "2001:db8:1:2::/64"},
		{"2001:db8:1:2:ffff::1", "2001:db8:1:2::/64"},
		{"fe80::1%eth0", "fe80::/64"},
	}
	for _, tt := range tests {
		if got := RateLimitKey(netip.MustParseAddr(tt.addr)); got.String() != tt.want {
			t.Errorf("RateLimitKey(%s) = %s, want %s", tt.addr, got, tt.want)
		}
	}
	if RateLimitKey(netip.Addr{}).IsValid() {
		t.Fatal("RateLimitKey(zero) is valid")
	}
}
