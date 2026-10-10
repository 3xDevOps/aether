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
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// A push service need not accept a body above 4096 octets (RFC 8291
	// section 4), and a message is one record.
	recordSize = 4096
	// maxPayload is what fits that record after its 86-octet header, the
	// padding delimiter and the 16-octet tag.
	maxPayload = recordSize - 86 - 1 - 16

	// messageTTL is how long a push service keeps a message for a device it
	// cannot reach.
	messageTTL = 24 * time.Hour
	// tokenTTL is the life of one VAPID token; RFC 8292 allows at most 24h.
	tokenTTL = 12 * time.Hour
	// vapidSubject is the contact a push service is given for this sender.
	// It names the project and no installation.
	vapidSubject = "https://github.com/3xDevOps/Aether"

	sendTimeout = 15 * time.Second
)

var b64 = base64.RawURLEncoding

// errGone means the push service no longer has the subscription.
var errGone = errors.New("the push service no longer has this subscription")

// vapidKey is the server's RFC 8292 identity: push services tie each
// subscription to the key the browser subscribed with.
type vapidKey struct {
	private *ecdsa.PrivateKey
	// public is the unpadded base64url uncompressed point browsers and push
	// services are given.
	public string
}

// loadOrCreateKey reads the key at path, generating a P-256 key (PKCS #8
// PEM, 0600, directory 0700) on first use.
func loadOrCreateKey(path string) (*vapidKey, error) {
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		block, _ := pem.Decode(raw)
		if block == nil {
			return nil, fmt.Errorf("push: %s is not a PEM file", path)
		}
		parsed, perr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if perr != nil {
			return nil, fmt.Errorf("push: parse key %s: %w", path, perr)
		}
		private, ok := parsed.(*ecdsa.PrivateKey)
		if !ok || private.Curve != elliptic.P256() {
			return nil, fmt.Errorf("push: %s is not a P-256 key", path)
		}
		return newVAPIDKey(private)
	case !os.IsNotExist(err):
		return nil, fmt.Errorf("push: read key %s: %w", path, err)
	}
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("push: generate key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, fmt.Errorf("push: marshal key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("push: create key dir: %w", err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, fmt.Errorf("push: write key %s: %w", path, err)
	}
	return newVAPIDKey(private)
}

func newVAPIDKey(private *ecdsa.PrivateKey) (*vapidKey, error) {
	public, err := private.PublicKey.ECDH()
	if err != nil {
		return nil, fmt.Errorf("push: public key: %w", err)
	}
	return &vapidKey{private: private, public: b64.EncodeToString(public.Bytes())}, nil
}

// authorization is the RFC 8292 header for one request to endpoint: an
// ES256 JWT naming the push service's origin, and the key that signed it.
func (k *vapidKey) authorization(endpoint *url.URL, now time.Time) (string, error) {
	claims, err := json.Marshal(struct {
		Audience string `json:"aud"`
		Expires  int64  `json:"exp"`
		Subject  string `json:"sub"`
	}{endpoint.Scheme + "://" + endpoint.Host, now.Add(tokenTTL).Unix(), vapidSubject})
	if err != nil {
		return "", err
	}
	unsigned := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + b64.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, k.private, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return "vapid t=" + unsigned + "." + b64.EncodeToString(signature) + ", k=" + k.public, nil
}

// target is a subscription decoded for sending.
type target struct {
	endpoint *url.URL
	key      *ecdh.PublicKey
	auth     []byte
}

// parseTarget validates a subscription as a browser hands it over. Only
// HTTPS endpoints are accepted: the server posts to this address.
func parseTarget(endpoint, p256dh, auth string) (*target, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("endpoint must be an https URL")
	}
	point, err := b64.DecodeString(strings.TrimRight(p256dh, "="))
	if err != nil {
		return nil, fmt.Errorf("keys.p256dh is not base64url: %w", err)
	}
	key, err := ecdh.P256().NewPublicKey(point)
	if err != nil {
		return nil, fmt.Errorf("keys.p256dh is not a P-256 public key: %w", err)
	}
	secret, err := b64.DecodeString(strings.TrimRight(auth, "="))
	if err != nil || len(secret) != 16 {
		return nil, errors.New("keys.auth must be 16 base64url octets")
	}
	return &target{endpoint: u, key: key, auth: secret}, nil
}

// encrypt is RFC 8291: payload as one aes128gcm record only the browser
// holding the subscription's private key can read.
func encrypt(payload []byte, to *target) ([]byte, error) {
	sender, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return seal(payload, to, sender, salt)
}

func seal(payload []byte, to *target, sender *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(payload) > maxPayload {
		return nil, fmt.Errorf("payload is %d octets, above the %d a push message carries", len(payload), maxPayload)
	}
	shared, err := sender.ECDH(to.key)
	if err != nil {
		return nil, err
	}
	senderPublic := sender.PublicKey().Bytes()
	info := "WebPush: info\x00" + string(to.key.Bytes()) + string(senderPublic)
	ikm, err := hkdf.Key(sha256.New, shared, to.auth, info, 32)
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
	out := make([]byte, 0, recordSize)
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, recordSize)
	out = append(out, byte(len(senderPublic)))
	out = append(out, senderPublic...)
	// 0x02 is the padding delimiter of a last record.
	plain := append(bytes.Clone(payload), 0x02)
	return gcm.Seal(out, nonce, plain, nil), nil
}

// send posts one message. topic, when set, replaces an undelivered message
// with the same topic at the push service.
func (k *vapidKey) send(ctx context.Context, client *http.Client, to *target, payload []byte, topic string) error {
	body, err := encrypt(payload, to)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	authorization, err := k.authorization(to.endpoint, time.Now())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, to.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(messageTTL/time.Second)))
	req.Header.Set("Urgency", "high")
	if topic != "" {
		req.Header.Set("Topic", topic)
	}
	resp, err := client.Do(req)
	if err != nil {
		// The endpoint's path is the browser's subscription; the host says
		// which push service could not be reached.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("%s: %w", to.endpoint.Host, err)
	}
	defer resp.Body.Close() //nolint:errcheck // nothing to do about a failed close of a read body
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return errGone
	}
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("%s answered %s: %s", to.endpoint.Host, resp.Status, strings.TrimSpace(string(detail)))
}

// newClient returns the client messages are sent with. A subscription's
// endpoint comes from a member's browser, so the server connects only to
// public addresses: nothing on its own host, LAN or tailnet. It connects
// directly, ignoring proxy settings, so that check sees the real peer.
func newClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: publicOnly}
	return &http.Client{Transport: &http.Transport{
		DialContext:         dialer.DialContext,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     90 * time.Second,
	}}
}

// sharedAddresses is RFC 6598 carrier-grade NAT space, which tailnets use.
var sharedAddresses = netip.MustParsePrefix("100.64.0.0/10")

func publicOnly(_, address string, _ syscall.RawConn) error {
	peer, err := netip.ParseAddrPort(address)
	if err != nil {
		return err
	}
	ip := peer.Addr().Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || sharedAddresses.Contains(ip) {
		return fmt.Errorf("refusing to connect to %s: not a public address", ip)
	}
	return nil
}
