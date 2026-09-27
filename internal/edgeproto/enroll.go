package edgeproto

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"

	"golang.org/x/crypto/ssh"
)

// NonceSize is the length of the enrollment challenge nonce.
const NonceSize = 32

const enrollContext = "aether-edge-enroll-v1\x00"

// Origin canonicalizes an edge URL to "scheme://host[:port]". The scheme is
// https; http is accepted only for a loopback host, for local testing.
func Origin(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("edgeproto: edge url %q: %w", rawURL, err)
	}
	if u.Host == "" || u.Opaque != "" || u.User != nil || (u.Path != "" && u.Path != "/") ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("edgeproto: edge url %q must be https://host[:port]", rawURL)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return "", fmt.Errorf("edgeproto: edge url %q must use https", rawURL)
		}
	default:
		return "", fmt.Errorf("edgeproto: edge url %q must use https", rawURL)
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

// EnrollmentMessage is what a server's host key signs to enroll:
// "aether-edge-enroll-v1\x00" || origin || "\x00" || server id || "\x00" || nonce.
// With a canonical origin, a valid server id and a NonceSize nonce it is
// always longer than 64 bytes, so it can never equal an SSH exchange hash.
func EnrollmentMessage(origin, serverID string, nonce []byte) ([]byte, error) {
	canonical, err := Origin(origin)
	if err != nil {
		return nil, err
	}
	if canonical != origin {
		return nil, fmt.Errorf("edgeproto: edge origin %q is not canonical (%q)", origin, canonical)
	}
	if !ValidServerID(serverID) {
		return nil, fmt.Errorf("edgeproto: invalid server id %q", serverID)
	}
	if len(nonce) != NonceSize {
		return nil, fmt.Errorf("edgeproto: enrollment nonce is %d bytes, want %d", len(nonce), NonceSize)
	}
	msg := make([]byte, 0, len(enrollContext)+len(origin)+len(serverID)+len(nonce)+2)
	msg = append(msg, enrollContext...)
	msg = append(msg, origin...)
	msg = append(msg, 0)
	msg = append(msg, serverID...)
	msg = append(msg, 0)
	msg = append(msg, nonce...)
	return msg, nil
}

// SignEnrollment signs the enrollment message for the server that owns
// signer and returns the SSH wire-format signature. origin must be the
// origin of the edge URL the server dialed, never the one the challenge
// names: a server signing a challenge relayed from another edge would
// enroll there.
func SignEnrollment(signer ssh.Signer, origin string, nonce []byte) ([]byte, error) {
	msg, err := EnrollmentMessage(origin, ServerID(signer.PublicKey()), nonce)
	if err != nil {
		return nil, err
	}
	var sig *ssh.Signature
	if signer.PublicKey().Type() == ssh.KeyAlgoRSA {
		algSigner, ok := signer.(ssh.AlgorithmSigner)
		if !ok {
			return nil, errors.New("edgeproto: RSA host key signer cannot sign with rsa-sha2-256")
		}
		sig, err = algSigner.SignWithAlgorithm(rand.Reader, msg, ssh.KeyAlgoRSASHA256)
	} else {
		sig, err = signer.Sign(rand.Reader, msg)
	}
	if err != nil {
		return nil, fmt.Errorf("edgeproto: sign enrollment: %w", err)
	}
	return ssh.Marshal(sig), nil
}

// VerifyEnrollment checks a hello's signature against the challenge the
// edge sent and returns the server id derived from hostKey.
func VerifyEnrollment(hostKey ssh.PublicKey, origin string, nonce, signature []byte) (string, error) {
	if _, ok := hostKey.(*ssh.Certificate); ok {
		return "", errors.New("edgeproto: host key is a certificate, want a plain public key")
	}
	serverID := ServerID(hostKey)
	msg, err := EnrollmentMessage(origin, serverID, nonce)
	if err != nil {
		return "", err
	}
	var sig ssh.Signature
	if err := ssh.Unmarshal(signature, &sig); err != nil {
		return "", fmt.Errorf("edgeproto: parse enrollment signature: %w", err)
	}
	if sig.Format == ssh.KeyAlgoRSA {
		return "", errors.New("edgeproto: enrollment signature uses ssh-rsa (SHA-1), want rsa-sha2-256")
	}
	if err := hostKey.Verify(msg, &sig); err != nil {
		return "", fmt.Errorf("edgeproto: enrollment signature: %w", err)
	}
	return serverID, nil
}
