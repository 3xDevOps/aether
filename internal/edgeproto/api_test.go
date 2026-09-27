package edgeproto

import (
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
	mux.HandleFunc("GET "+PathServerData, func(_ http.ResponseWriter, r *http.Request) { got = "data " + r.PathValue("conn_id") })

	id, conn := "wqc4lsjvzdzrwq3k5dabdtajwj", NewConnID()
	for path, want := range map[string]string{
		ConnectPath(id): "connect " + id,
		DataPath(conn):  "data " + conn,
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

func TestPKCE(t *testing.T) {
	// RFC 7636 appendix B.
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := PKCEChallenge(verifier); got != challenge {
		t.Fatalf("PKCEChallenge = %q, want %q", got, challenge)
	}
	if !VerifyPKCE(challenge, verifier) {
		t.Fatal("VerifyPKCE rejected the RFC vector")
	}
	for _, bad := range []string{"", verifier[1:], NewVerifier(), challenge} {
		if VerifyPKCE(challenge, bad) {
			t.Errorf("VerifyPKCE accepted verifier %q", bad)
		}
	}
	if v := NewVerifier(); !ValidToken(v) || !ValidToken(PKCEChallenge(v)) {
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
