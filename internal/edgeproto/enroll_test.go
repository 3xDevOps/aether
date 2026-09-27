package edgeproto

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

const testOrigin = "https://edge.example"

func testNonce(fill byte) []byte { return bytes.Repeat([]byte{fill}, NonceSize) }

func TestOrigin(t *testing.T) {
	tests := []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{"https://edge.example", "https://edge.example", false},
		{"https://Edge.Example/", "https://edge.example", false},
		{"HTTPS://edge.example:8443", "https://edge.example:8443", false},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080", false},
		{"http://[::1]:8080", "http://[::1]:8080", false},
		{"http://localhost:8080", "http://localhost:8080", false},
		{"http://edge.example", "", true},
		{"http://10.0.0.1", "", true},
		{"wss://edge.example", "", true},
		{"https://edge.example/v1", "", true},
		{"https://edge.example?x=1", "", true},
		{"https://edge.example?", "", true},
		{"https://edge.example#x", "", true},
		{"https://user@edge.example", "", true},
		{"https://", "", true},
		{"edge.example", "", true},
		{"", "", true},
		{"https://edge.example\x00x", "", true},
	}
	for _, tt := range tests {
		got, err := Origin(tt.raw)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("Origin(%q) = %q, %v; want %q, error %v", tt.raw, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestEnrollmentMessage(t *testing.T) {
	id := ServerID(seedSigner(t, 1).PublicKey())
	msg, err := EnrollmentMessage(testOrigin, id, testNonce(7))
	if err != nil {
		t.Fatal(err)
	}
	want := "aether-edge-enroll-v1\x00https://edge.example\x00" + id + "\x00" + string(testNonce(7))
	if string(msg) != want {
		t.Fatalf("EnrollmentMessage = %q, want %q", msg, want)
	}

	tests := []struct {
		name   string
		origin string
		id     string
		nonce  []byte
	}{
		{"empty origin", "", id, testNonce(1)},
		{"non-canonical origin", "https://Edge.example/", id, testNonce(1)},
		{"origin with NUL", "https://edge.example\x00" + id, id, testNonce(1)},
		{"bad server id", testOrigin, "not-an-id", testNonce(1)},
		{"empty nonce", testOrigin, id, nil},
		{"short nonce", testOrigin, id, make([]byte, NonceSize-1)},
		{"long nonce", testOrigin, id, make([]byte, NonceSize+1)},
	}
	for _, tt := range tests {
		if _, err := EnrollmentMessage(tt.origin, tt.id, tt.nonce); err == nil {
			t.Errorf("%s: EnrollmentMessage accepted it", tt.name)
		}
	}
}

// The host key also signs SSH exchange hashes (32 bytes for SHA-256 kex, 64
// for SHA-512). No enrollment message may be that short.
func TestEnrollmentMessageNeverSSHSized(t *testing.T) {
	id := ServerID(seedSigner(t, 1).PublicKey())
	shortest := "http://[::1]"
	if _, err := Origin(shortest); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{shortest, "https://a", testOrigin} {
		for n := 0; n <= 128; n++ {
			msg, err := EnrollmentMessage(origin, id, make([]byte, n))
			if err != nil {
				continue
			}
			if n != NonceSize {
				t.Fatalf("accepted a %d-byte nonce", n)
			}
			if len(msg) <= 64 {
				t.Fatalf("message for origin %q is %d bytes", origin, len(msg))
			}
			if !bytes.HasPrefix(msg, []byte("aether-edge-enroll-v1\x00")) {
				t.Fatal("message lacks the context string")
			}
		}
	}
}

func TestEnrollmentRoundTrip(t *testing.T) {
	signer := seedSigner(t, 1)
	sig, err := SignEnrollment(signer, testOrigin, testNonce(1))
	if err != nil {
		t.Fatal(err)
	}
	id, err := VerifyEnrollment(signer.PublicKey(), testOrigin, testNonce(1), sig)
	if err != nil {
		t.Fatal(err)
	}
	if id != ServerID(signer.PublicKey()) {
		t.Fatalf("VerifyEnrollment id = %q", id)
	}

	other := seedSigner(t, 2)
	tests := []struct {
		name   string
		key    ssh.PublicKey
		origin string
		nonce  []byte
		sig    []byte
	}{
		{"other host key", other.PublicKey(), testOrigin, testNonce(1), sig},
		{"other edge origin", signer.PublicKey(), "https://other-edge.example", testNonce(1), sig},
		{"other nonce", signer.PublicKey(), testOrigin, testNonce(2), sig},
		{"truncated signature", signer.PublicKey(), testOrigin, testNonce(1), sig[:len(sig)-1]},
		{"garbage signature", signer.PublicKey(), testOrigin, testNonce(1), []byte("not a signature")},
		{"empty signature", signer.PublicKey(), testOrigin, testNonce(1), nil},
	}
	for _, tt := range tests {
		if _, err := VerifyEnrollment(tt.key, tt.origin, tt.nonce, tt.sig); err == nil {
			t.Errorf("%s: VerifyEnrollment accepted it", tt.name)
		}
	}
}

// An edge that picks the nonce must not obtain a signature it can present
// in an SSH handshake: the signature is never valid over the raw nonce, nor
// over any 32- or 64-byte value such as an exchange hash.
func TestEnrollmentIsNotASigningOracle(t *testing.T) {
	signer := seedSigner(t, 1)
	sha256Hash := sha256.Sum256([]byte("attacker-chosen exchange"))
	sha512Hash := sha512.Sum512([]byte("attacker-chosen exchange"))
	for _, raw := range [][]byte{sha256Hash[:], sha512Hash[:], sha512Hash[:NonceSize]} {
		if len(raw) != NonceSize {
			if _, err := SignEnrollment(signer, testOrigin, raw); err == nil {
				t.Fatalf("signed a %d-byte nonce", len(raw))
			}
			continue
		}
		wire, err := SignEnrollment(signer, testOrigin, raw)
		if err != nil {
			t.Fatal(err)
		}
		var sig ssh.Signature
		if err := ssh.Unmarshal(wire, &sig); err != nil {
			t.Fatal(err)
		}
		if err := signer.PublicKey().Verify(raw, &sig); err == nil {
			t.Fatal("enrollment signature verifies over the raw nonce")
		}
		for _, h := range [][]byte{sha256Hash[:], sha512Hash[:]} {
			if err := signer.PublicKey().Verify(h, &sig); err == nil {
				t.Fatalf("enrollment signature verifies over a %d-byte hash", len(h))
			}
		}
	}
}

func TestEnrollmentCertificateRefused(t *testing.T) {
	host, ca := seedSigner(t, 1), seedSigner(t, 2)
	cert := &ssh.Certificate{
		Key:         host.PublicKey(),
		CertType:    ssh.HostCert,
		ValidBefore: uint64(time.Now().Add(time.Hour).Unix()),
	}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		t.Fatal(err)
	}
	certSigner, err := ssh.NewCertSigner(cert, host)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := SignEnrollment(host, testOrigin, testNonce(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyEnrollment(certSigner.PublicKey(), testOrigin, testNonce(1), sig); err == nil ||
		!strings.Contains(err.Error(), "certificate") {
		t.Fatalf("VerifyEnrollment(certificate) = %v", err)
	}
}

func TestEnrollmentRSA(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := SignEnrollment(signer, testOrigin, testNonce(1))
	if err != nil {
		t.Fatal(err)
	}
	var sig ssh.Signature
	if err = ssh.Unmarshal(wire, &sig); err != nil {
		t.Fatal(err)
	}
	if sig.Format != ssh.KeyAlgoRSASHA256 {
		t.Fatalf("RSA enrollment signature format = %q", sig.Format)
	}
	if _, err = VerifyEnrollment(signer.PublicKey(), testOrigin, testNonce(1), wire); err != nil {
		t.Fatal(err)
	}

	msg, err := EnrollmentMessage(testOrigin, ServerID(signer.PublicKey()), testNonce(1))
	if err != nil {
		t.Fatal(err)
	}
	sha1Sig, err := signer.(ssh.AlgorithmSigner).SignWithAlgorithm(rand.Reader, msg, ssh.KeyAlgoRSA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyEnrollment(signer.PublicKey(), testOrigin, testNonce(1), ssh.Marshal(sha1Sig)); err == nil {
		t.Fatal("VerifyEnrollment accepted an ssh-rsa (SHA-1) signature")
	}
}

func TestCheckVersion(t *testing.T) {
	if err := CheckVersion(Version); err != nil {
		t.Fatal(err)
	}
	if err := CheckVersion(Version + 1); err != nil {
		t.Fatalf("newer peer refused: %v", err)
	}
	for _, v := range []int{MinVersion - 1, -1} {
		err := CheckVersion(v)
		if want := fmt.Sprintf("upgrade required: %d", MinVersion); err == nil || err.Error() != want {
			t.Fatalf("CheckVersion(%d) = %v, want %q", v, err, want)
		}
	}
}
