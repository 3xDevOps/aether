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
		Hello{Version: Version, HostKey: signer.PublicKey().Marshal(), Signature: sig, Name: "devbox", AccessPolicy: PolicyAccount},
		Hello{Version: Version, HostKey: signer.PublicKey().Marshal(), Signature: sig, Name: "devbox", AccessPolicy: PolicyApprovedDevices},
		Ready{ServerID: ServerID(signer.PublicKey()), State: StateUnclaimed, EdgeKey: edgeKey(1).Public().(ed25519.PublicKey)},
		Ready{ServerID: ServerID(signer.PublicKey()), State: StateClaimed, EdgeKey: edgeKey(1).Public().(ed25519.PublicKey)},
		Open{ConnID: conn, Ticket: NewToken(), Kind: KindSSH, Grant: "payload.sig", ClientAddr: "192.0.2.1:5555"},
		Open{ConnID: conn, Ticket: NewToken(), Kind: KindClaim, Grant: "payload.sig"},
		OpenResult{ConnID: conn},
		OpenResult{ConnID: conn, Error: "device is pending approval: run aether device approve fake"},
		Claimed{ConnID: conn, Owner: Principal{Type: PrincipalAccount, Provider: ProviderGoogle, Subject: "fake-sub"}},
		OwnerTransferred{ID: conn, Owner: Principal{Type: PrincipalAccount, Provider: ProviderGitHub, Subject: "1002"}},
		OwnerTransferResult{ID: conn, Owner: Principal{Type: PrincipalAccount, Provider: ProviderGitHub, Subject: "1002"}},
		OwnerTransferResult{ID: conn, Owner: Principal{Type: PrincipalAccount, Provider: ProviderGitHub, Subject: "1002"},
			Error: "ownership report refused: account is blocked by this edge's operator"},
		Ownerless{},
		Directory{Entries: []DirectoryEntry{
			{Kind: EntryMember, Provider: ProviderGitHub, Subject: "1001", Role: "admin"},
			{Kind: EntryInvitation, Email: "new@example.com", Role: "viewer", ExpiresAt: exp},
		}},
		Directory{},
		DeviceRevoked{DeviceID: "dev-fake-1"},
		AccountDeleted{Provider: ProviderGitHub, Subject: "1001"},
		AccountDeletionApplied{Provider: ProviderGitHub, Subject: "1001"},
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
	hostKey := seedSigner(t, 1).PublicKey().Marshal()
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
		{"ready with unknown state", `{"type":"ready","server_id":"wqc4lsjvzdzrwq3k5dabdtajwj","state":"owned","edge_key":"` + b64(edgeKey(1).Public().(ed25519.PublicKey)) + `"}`, false},
		{"open with path in conn id", `{"type":"open","conn_id":"../../v1/connect/x","ticket":"` + NewToken() + `","kind":"ssh","grant":"a.b"}`, false},
		{"ssh open without grant", `{"type":"open","conn_id":"` + conn + `","ticket":"` + NewToken() + `","kind":"ssh"}`, false},
		{"open with bad ticket", `{"type":"open","conn_id":"` + conn + `","ticket":"short","kind":"ssh","grant":"a.b"}`, false},
		{"open with log-injecting address", `{"type":"open","conn_id":"` + conn + `","ticket":"` + NewToken() + `","kind":"ssh","grant":"a.b","client_addr":"1.2.3.4:5\nforged log line"}`, false},
		{"open of unknown kind", `{"type":"open","conn_id":"` + conn + `","ticket":"` + NewToken() + `","kind":"admin","grant":"a.b"}`, false},
		{"claim open without grant", `{"type":"open","conn_id":"` + conn + `","ticket":"` + NewToken() + `","kind":"claim"}`, false},
		{"hello with unknown policy", `{"type":"hello","version":1,"host_key":"` + b64(hostKey) + `","signature":"AAAA","access_policy":"anyone"}`, false},
		{"web open, a kind no longer carried", `{"type":"open","conn_id":"` + conn + `","ticket":"` + NewToken() + `","kind":"web"}`, false},
		{"result with escape in error", `{"type":"open_result","conn_id":"` + conn + `","error":"\u001b[2J"}`, false},
		{"claim, which the edge no longer sends", `{"type":"claim","id":"` + conn + `","code":"wqc4lsjvabcdefghijklmnopqr-abcdefghijklmnop","grant":"a.b"}`, true},
		{"claim result, which no server sends", `{"type":"claim_result","id":"` + conn + `"}`, true},
		{"claimed without owner", `{"type":"claimed","conn_id":"` + conn + `","owner":{}}`, false},
		{"claimed without conn id", `{"type":"claimed","owner":{"type":"account","provider":"github","subject":"1"}}`, false},
		{"claimed by a team", `{"type":"claimed","conn_id":"` + conn + `","owner":{"type":"team","provider":"github","subject":"1"}}`, false},
		{"transferred to a team", `{"type":"owner_transferred","id":"` + conn + `","owner":{"type":"team"}}`, false},
		{"transferred to nobody", `{"type":"owner_transferred","id":"` + conn + `","owner":{"type":"account"}}`, false},
		{"transfer without id", `{"type":"owner_transferred","owner":{"type":"account","provider":"github","subject":"1"}}`, false},
		{"transfer with path in id", `{"type":"owner_transferred","id":"../` + conn[3:] + `","owner":{"type":"account","provider":"github","subject":"1"}}`, false},
		{"transfer result without id", `{"type":"owner_transfer_result","owner":{"type":"account","provider":"github","subject":"1"}}`, false},
		{"account deleted without subject", `{"type":"account_deleted","provider":"github"}`, false},
		{"account deleted with unknown provider", `{"type":"account_deleted","provider":"gitlab","subject":"1"}`, false},
		{"deletion applied without subject", `{"type":"account_deletion_applied","provider":"github"}`, false},
		{"transfer result for nobody", `{"type":"owner_transfer_result","id":"` + conn + `","owner":{"type":"account"}}`, false},
		{"transfer result with escape in error", `{"type":"owner_transfer_result","id":"` + conn + `","owner":{"type":"account","provider":"github","subject":"1"},"error":"\u001b[2J"}`, false},
		{"directory with bad entry", `{"type":"directory","entries":[{"kind":"member","provider":"github","role":"admin"}]}`, false},
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
	if _, err := EncodeControl(Claimed{ConnID: NewConnID(), Owner: Principal{Type: PrincipalTeam}}); err == nil {
		t.Fatal("EncodeControl sent a claim by a team")
	}
}

// A server that predates access policies sends no access_policy; its
// hello decodes, and the policy it stands for is the stricter one.
func TestHelloWithoutPolicy(t *testing.T) {
	signer := seedSigner(t, 1)
	data := `{"type":"hello","version":1,"host_key":"` + b64(signer.PublicKey().Marshal()) + `","signature":"AAAA","name":"devbox"}`
	m, err := DecodeControl([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParseAccessPolicy(string(m.(Hello).AccessPolicy))
	if err != nil || p != PolicyApprovedDevices {
		t.Fatalf("policy of a hello without one = %q, %v; want %q", p, err, PolicyApprovedDevices)
	}
}

func b64(b []byte) string {
	data, _ := json.Marshal(b)
	return strings.Trim(string(data), `"`)
}
