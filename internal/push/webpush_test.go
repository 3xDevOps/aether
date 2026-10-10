package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// browser is the receiving side of a subscription: the keys a browser
// generates, and RFC 8291 decryption with them.
type browser struct {
	private *ecdh.PrivateKey
	auth    []byte
}

func newBrowser(t *testing.T) *browser {
	t.Helper()
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate browser key: %v", err)
	}
	auth := make([]byte, 16)
	if _, err = rand.Read(auth); err != nil {
		t.Fatalf("generate auth secret: %v", err)
	}
	return &browser{private: private, auth: auth}
}

func (b *browser) p256dh() string  { return b64.EncodeToString(b.private.PublicKey().Bytes()) }
func (b *browser) authKey() string { return b64.EncodeToString(b.auth) }

// open decrypts one aes128gcm push message body as the browser would.
func (b *browser) open(body []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("body shorter than the aes128gcm header")
	}
	salt, keyLength := body[:16], int(body[20])
	if size := binary.BigEndian.Uint32(body[16:20]); int(size) < len(body)-21-keyLength {
		return nil, errors.New("record size is smaller than the record")
	}
	if len(body) < 21+keyLength {
		return nil, errors.New("body shorter than its key id")
	}
	sender, err := ecdh.P256().NewPublicKey(body[21 : 21+keyLength])
	if err != nil {
		return nil, err
	}
	shared, err := b.private.ECDH(sender)
	if err != nil {
		return nil, err
	}
	info := "WebPush: info\x00" + string(b.private.PublicKey().Bytes()) + string(sender.Bytes())
	ikm, err := hkdf.Key(sha256.New, shared, b.auth, info, 32)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, body[21+keyLength:], nil)
	if err != nil {
		return nil, err
	}
	plain = bytes.TrimRight(plain, "\x00")
	if len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		return nil, errors.New("record does not end with the last-record delimiter")
	}
	return plain[:len(plain)-1], nil
}

func decode(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := b64.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return raw
}

// The example of RFC 8291 section 5, with the inputs of its appendix A.
func TestSealMatchesRFC8291Example(t *testing.T) {
	t.Parallel()
	sender, err := ecdh.P256().NewPrivateKey(decode(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatalf("sender key: %v", err)
	}
	to, err := parseTarget("https://push.example.net/push/JzLQ3raZJfFBR0aqvOMsLrt54w4rJUsV",
		"BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
		"BTBZMqHH6r4Tts7J_aSIgg")
	if err != nil {
		t.Fatalf("parseTarget: %v", err)
	}
	got, err := seal([]byte("When I grow up, I want to be a watermelon"), to, sender, decode(t, "DGv6ra1nlYgDCS1FRnbzlw"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	const want = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if b64.EncodeToString(got) != want {
		t.Fatalf("seal = %s\nwant   %s", b64.EncodeToString(got), want)
	}
}

// One request to a fake push service, checked the way a real one and the
// browser behind it would: the RFC 8030 headers, the RFC 8292 token against
// the key it names, and the RFC 8291 body decrypted with the subscription's
// own keys.
func TestSendIsAStandardWebPushRequest(t *testing.T) {
	t.Parallel()
	phone := newBrowser(t)
	key, err := loadOrCreateKey(filepath.Join(t.TempDir(), "push", "vapid_key.pem"))
	if err != nil {
		t.Fatalf("loadOrCreateKey: %v", err)
	}
	type request struct {
		*http.Request
		body []byte
	}
	requests := make(chan request, 1)
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- request{r, body}
		w.WriteHeader(http.StatusCreated)
	}))
	defer service.Close()
	to, err := parseTarget(service.URL+"/send/abc", phone.p256dh(), phone.authKey())
	if err != nil {
		t.Fatalf("parseTarget: %v", err)
	}
	payload := []byte(`{"title":"Fix the login redirect","body":"Waiting for your reply","run":"run-7k2m9q4xbd"}`)
	if err = key.send(context.Background(), service.Client(), to, payload, "topic-1"); err != nil {
		t.Fatalf("send: %v", err)
	}
	got := <-requests
	body := got.body

	if got.Method != http.MethodPost || got.URL.Path != "/send/abc" {
		t.Fatalf("request = %s %s, want POST /send/abc", got.Method, got.URL.Path)
	}
	for header, want := range map[string]string{
		"Content-Encoding": "aes128gcm",
		"Content-Type":     "application/octet-stream",
		"TTL":              "86400",
		"Urgency":          "high",
		"Topic":            "topic-1",
	} {
		if got.Header.Get(header) != want {
			t.Errorf("%s = %q, want %q", header, got.Header.Get(header), want)
		}
	}

	token, public, ok := strings.Cut(strings.TrimPrefix(got.Header.Get("Authorization"), "vapid t="), ", k=")
	if !ok || public != key.public {
		t.Fatalf("Authorization = %q, want a vapid token and the server's key", got.Header.Get("Authorization"))
	}
	signer, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), decode(t, public))
	if err != nil {
		t.Fatalf("parse k: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts, want 3", len(parts))
	}
	signature := decode(t, parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(signature) != 64 || !ecdsa.Verify(signer, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		t.Fatal("the token's ES256 signature does not verify against k")
	}
	var header struct{ Typ, Alg string }
	var claims struct {
		Aud, Sub string
		Exp      int64
	}
	if err = json.Unmarshal(decode(t, parts[0]), &header); err != nil || header.Typ != "JWT" || header.Alg != "ES256" {
		t.Fatalf("token header = %+v (%v), want JWT ES256", header, err)
	}
	if err = json.Unmarshal(decode(t, parts[1]), &claims); err != nil {
		t.Fatalf("token claims: %v", err)
	}
	life := time.Until(time.Unix(claims.Exp, 0))
	if claims.Aud != service.URL || claims.Sub != vapidSubject || life <= 0 || life > 24*time.Hour {
		t.Fatalf("claims = %+v, want aud %s, the project as sub and an expiry within 24h", claims, service.URL)
	}

	opened, err := phone.open(body)
	if err != nil {
		t.Fatalf("decrypt with the subscription's keys: %v", err)
	}
	if !bytes.Equal(opened, payload) {
		t.Fatalf("decrypted = %s, want %s", opened, payload)
	}
	if len(body) > recordSize {
		t.Fatalf("body is %d octets, above the %d a push service must accept", len(body), recordSize)
	}
}

func TestSendReportsWhatThePushServiceSaid(t *testing.T) {
	t.Parallel()
	phone := newBrowser(t)
	key, err := loadOrCreateKey(filepath.Join(t.TempDir(), "vapid_key.pem"))
	if err != nil {
		t.Fatalf("loadOrCreateKey: %v", err)
	}
	status := http.StatusGone
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"reason":"BadJwtToken"}`, status)
	}))
	to, err := parseTarget(service.URL+"/send/abc", phone.p256dh(), phone.authKey())
	if err != nil {
		t.Fatalf("parseTarget: %v", err)
	}
	host := strings.TrimPrefix(service.URL, "https://")

	if err = key.send(context.Background(), service.Client(), to, []byte("{}"), ""); !errors.Is(err, errGone) {
		t.Fatalf("send to a 410 = %v, want errGone", err)
	}
	status = http.StatusForbidden
	err = key.send(context.Background(), service.Client(), to, []byte("{}"), "")
	if want := host + ` answered 403 Forbidden: {"reason":"BadJwtToken"}`; err == nil || err.Error() != want {
		t.Fatalf("send to a 403 = %v, want %q", err, want)
	}
	service.Close()
	err = key.send(context.Background(), service.Client(), to, []byte("{}"), "")
	if err == nil || !strings.HasPrefix(err.Error(), host+": dial tcp") || strings.Contains(err.Error(), "/send/abc") {
		t.Fatalf("send to a closed service = %v, want the dial error without the subscription's path", err)
	}
}

func TestKeyIsCreatedOnceAndPrivate(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "push", "vapid_key.pem")
	first, err := loadOrCreateKey(path)
	if err != nil {
		t.Fatalf("loadOrCreateKey: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v (%v), want 0600", info.Mode().Perm(), err)
	}
	if point := decode(t, first.public); len(point) != 65 || point[0] != 0x04 {
		t.Fatalf("public key is %d octets starting %#x, want a 65-octet uncompressed point", len(point), point[0])
	}
	second, err := loadOrCreateKey(path)
	if err != nil || second.public != first.public {
		t.Fatalf("second load = %v, %v; want the first key again", second, err)
	}
}

func TestParseTargetRefusesWhatABrowserWouldNotSend(t *testing.T) {
	t.Parallel()
	phone := newBrowser(t)
	for name, sub := range map[string][3]string{
		"plain http":   {"http://push.example/x", phone.p256dh(), phone.authKey()},
		"no host":      {"https:///x", phone.p256dh(), phone.authKey()},
		"short key":    {"https://push.example/x", "BCVx", phone.authKey()},
		"off curve":    {"https://push.example/x", b64.EncodeToString(append([]byte{4}, make([]byte, 64)...)), phone.authKey()},
		"short secret": {"https://push.example/x", phone.p256dh(), "c2hvcnQ"},
	} {
		if _, err := parseTarget(sub[0], sub[1], sub[2]); err == nil {
			t.Errorf("%s: parseTarget accepted %v", name, sub)
		}
	}
}

func TestClientConnectsOnlyToPublicAddresses(t *testing.T) {
	t.Parallel()
	for address, public := range map[string]bool{
		"142.250.80.106:443":             true,
		"[2607:f8b0:4006:80e::200a]:443": true,
		"127.0.0.1:443":                  false,
		"[::1]:443":                      false,
		"10.0.0.5:443":                   false,
		"192.168.1.10:443":               false,
		"169.254.169.254:443":            false,
		"100.101.102.103:443":            false,
		"[fd7a:115c:a1e0::1]:443":        false,
		"[::ffff:10.0.0.5]:443":          false,
		"0.0.0.0:443":                    false,
	} {
		if err := publicOnly("tcp", address, nil); (err == nil) != public {
			t.Errorf("publicOnly(%s) = %v, want public=%v", address, err, public)
		}
	}
}
