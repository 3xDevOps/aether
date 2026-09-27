package edgeproto

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validMessages(t *testing.T) []Message {
	t.Helper()
	signer := seedSigner(t, 1)
	sig, err := SignEnrollment(signer, testOrigin, testNonce(1))
	if err != nil {
		t.Fatal(err)
	}
	conn := NewConnID()
	exp := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	return []Message{
		Challenge{Version: Version, Nonce: testNonce(1), Origin: testOrigin},
		Hello{Version: Version, HostKey: signer.PublicKey().Marshal(), Signature: sig, AgentVersion: "v0.0.0-test", Name: "devbox"},
		Ready{ServerID: ServerID(signer.PublicKey()), State: StateUnclaimed, EdgeKey: edgeKey(1).Public().(ed25519.PublicKey)},
		Ready{ServerID: ServerID(signer.PublicKey()), State: StateClaimed, EdgeKey: edgeKey(1).Public().(ed25519.PublicKey), ServerDomain: "servers.example.test"},
		Open{ConnID: conn, Ticket: NewToken(), Kind: KindSSH, Grant: "payload.sig", ClientAddr: "192.0.2.1:5555"},
		Open{ConnID: conn, Ticket: NewToken(), Kind: KindWeb},
		OpenResult{ConnID: conn},
		OpenResult{ConnID: conn, Error: "device is pending approval: run aether device approve fake"},
		Claim{ID: conn, Code: "wqc4lsjv-abcdefghijklmnop", Grant: "payload.sig"},
		ClaimResult{ID: conn, Error: string(RefusalClaimWrong)},
		Claimed{Owner: Account{Provider: ProviderGoogle, Subject: "fake-sub", Email: "owner@example.com"}},
		Directory{Entries: []DirectoryEntry{
			{Kind: EntryMember, Provider: ProviderGitHub, Subject: "1001", Role: "admin"},
			{Kind: EntryInvitation, Email: "new@example.com", Role: "viewer", ExpiresAt: exp},
		}},
		Directory{},
		WebRedeem{ID: conn, Code: NewToken(), Verifier: NewVerifier()},
		WebRedeemResult{ID: conn, Grant: "payload.sig"},
		WebRedeemResult{ID: conn, Error: "code expired"},
		DeviceRevoked{DeviceID: "dev-fake-1"},
		Unenroll{},
		Drain{},
		Ping{},
		Pong{},
	}
}

func TestControlRoundTrip(t *testing.T) {
	for _, m := range validMessages(t) {
		data, err := EncodeControl(m)
		if err != nil {
			t.Fatalf("EncodeControl(%T) = %v", m, err)
		}
		if !bytes.HasPrefix(data, []byte(`{"type":"`+m.controlType()+`"`)) || !json.Valid(data) {
			t.Fatalf("EncodeControl(%T) = %s", m, data)
		}
		got, err := DecodeControl(data)
		if err != nil {
			t.Fatalf("DecodeControl(%s) = %v", data, err)
		}
		if reflect.TypeOf(got) != reflect.TypeOf(m) {
			t.Fatalf("DecodeControl(%s) = %T, want %T", data, got, m)
		}
		if d, ok := m.(Directory); ok && len(d.Entries) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, m) {
			t.Fatalf("round trip of %T = %+v, want %+v", m, got, m)
		}
	}
}

func TestControlEnvelope(t *testing.T) {
	data, err := EncodeControl(Ping{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"type":"ping"}` {
		t.Fatalf("EncodeControl(Ping) = %s", data)
	}
	data, err = EncodeControl(DeviceRevoked{DeviceID: "dev-fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"type":"device_revoked","device_id":"dev-fake-1"}` {
		t.Fatalf("EncodeControl(DeviceRevoked) = %s", data)
	}
}

func TestDecodeControlRefusals(t *testing.T) {
	conn := NewConnID()
	tests := []struct {
		name    string
		data    string
		unknown bool
	}{
		{"not json", `{"type":`, false},
		{"no type", `{"conn_id":"x"}`, true},
		{"unknown type", `{"type":"teleport"}`, true},
		{"short nonce", `{"type":"challenge","version":1,"nonce":"AAAA","origin":"https://edge.example"}`, false},
		{"non-canonical origin", `{"type":"challenge","version":1,"nonce":"` + b64(testNonce(1)) + `","origin":"https://EDGE.example/"}`, false},
		{"hello with garbage key", `{"type":"hello","version":1,"host_key":"AAAA","signature":"AAAA"}`, false},
		{"ready with short edge key", `{"type":"ready","server_id":"wqc4lsjvzdzrwq3k5dabdtajwj","state":"claimed","edge_key":"AAAA"}`, false},
		{"ready with uppercase server domain", `{"type":"ready","server_id":"wqc4lsjvzdzrwq3k5dabdtajwj","state":"claimed","edge_key":"` + b64(edgeKey(1).Public().(ed25519.PublicKey)) + `","server_domain":"Servers.example.test"}`, false},
		{"ready with single-label server domain", `{"type":"ready","server_id":"wqc4lsjvzdzrwq3k5dabdtajwj","state":"claimed","edge_key":"` + b64(edgeKey(1).Public().(ed25519.PublicKey)) + `","server_domain":"localhost"}`, false},
		{"ready with unknown state", `{"type":"ready","server_id":"wqc4lsjvzdzrwq3k5dabdtajwj","state":"owned","edge_key":"` + b64(edgeKey(1).Public().(ed25519.PublicKey)) + `"}`, false},
		{"open with path in conn id", `{"type":"open","conn_id":"../../v1/connect/x","ticket":"` + NewToken() + `","kind":"web"}`, false},
		{"ssh open without grant", `{"type":"open","conn_id":"` + conn + `","ticket":"` + NewToken() + `","kind":"ssh"}`, false},
		{"web open with grant", `{"type":"open","conn_id":"` + conn + `","ticket":"` + NewToken() + `","kind":"web","grant":"a.b"}`, false},
		{"open with bad ticket", `{"type":"open","conn_id":"` + conn + `","ticket":"short","kind":"web"}`, false},
		{"open with log-injecting address", `{"type":"open","conn_id":"` + conn + `","ticket":"` + NewToken() + `","kind":"web","client_addr":"1.2.3.4:5\nforged log line"}`, false},
		{"open of unknown kind", `{"type":"open","conn_id":"` + conn + `","ticket":"` + NewToken() + `","kind":"claim","grant":"a.b"}`, false},
		{"result with escape in error", `{"type":"open_result","conn_id":"` + conn + `","error":"\u001b[2J"}`, false},
		{"claim with bad code", `{"type":"claim","id":"` + conn + `","code":"nope","grant":"a.b"}`, false},
		{"claim without grant", `{"type":"claim","id":"` + conn + `","code":"wqc4lsjv-abcdefghijklmnop"}`, false},
		{"claimed without owner", `{"type":"claimed","owner":{}}`, false},
		{"directory with bad entry", `{"type":"directory","entries":[{"kind":"member","provider":"github","role":"admin"}]}`, false},
		{"redeem with bad verifier", `{"type":"web_redeem","id":"` + conn + `","code":"` + NewToken() + `","verifier":"x"}`, false},
		{"redeem result with both", `{"type":"web_redeem_result","id":"` + conn + `","grant":"a.b","error":"x"}`, false},
		{"redeem result with neither", `{"type":"web_redeem_result","id":"` + conn + `"}`, false},
		{"device revoked without id", `{"type":"device_revoked"}`, false},
	}
	for _, tt := range tests {
		_, err := DecodeControl([]byte(tt.data))
		if err == nil {
			t.Errorf("%s: DecodeControl accepted it", tt.name)
			continue
		}
		if errors.Is(err, ErrUnknownMessage) != tt.unknown {
			t.Errorf("%s: DecodeControl = %v, unknown type %v", tt.name, err, tt.unknown)
		}
	}
}

func TestControlSizeLimits(t *testing.T) {
	big := []byte(`{"type":"ping","pad":"` + strings.Repeat("a", MaxControlMessageSize) + `"}`)
	if _, err := DecodeControl(big); err == nil {
		t.Fatal("DecodeControl accepted an oversize message")
	}

	entries := make([]DirectoryEntry, MaxDirectoryEntries+1)
	for i := range entries {
		entries[i] = DirectoryEntry{Kind: EntryMember, Provider: ProviderGitHub, Subject: "1", Role: "admin"}
	}
	if _, err := EncodeControl(Directory{Entries: entries}); err == nil {
		t.Fatal("EncodeControl accepted too many directory entries")
	}

	long := strings.Repeat("x", maxShortText)
	for i := range entries {
		entries[i].Subject = long
	}
	if _, err := EncodeControl(Directory{Entries: entries[:MaxDirectoryEntries]}); err != nil {
		t.Fatalf("largest directory does not encode: %v", err)
	}
}

func TestEncodeControlValidates(t *testing.T) {
	if _, err := EncodeControl(Open{ConnID: NewConnID(), Ticket: NewToken(), Kind: KindSSH}); err == nil {
		t.Fatal("EncodeControl sent an ssh open without a grant")
	}
}

func b64(b []byte) string {
	data, _ := json.Marshal(b)
	return strings.Trim(string(data), `"`)
}
