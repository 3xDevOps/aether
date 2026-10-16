package selfupdate

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"testing"
)

// releaseKeys are the ed25519 public keys a release manifest may be signed
// with. A manifest verifies when any one of them signed it, so a rotation
// ships the next key here one release before releases are signed with it.
var releaseKeys = mustDecodeKeys(
	"J+AIq6GUVbRtEQ1ORqKJe3uh9JhvZkEQJQtYpxKbxD8=",
)

func mustDecodeKeys(encoded ...string) []ed25519.PublicKey {
	keys := make([]ed25519.PublicKey, 0, len(encoded))
	for _, s := range encoded {
		raw, err := base64.StdEncoding.DecodeString(s)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			panic(fmt.Sprintf("selfupdate: release key %q is not a base64 ed25519 public key", s))
		}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	return keys
}

// TrustKeysForTest replaces the release keys until the returned function
// runs, so tests can serve releases signed by a key of their own. It
// panics outside a test binary.
func TrustKeysForTest(keys ...ed25519.PublicKey) (restore func()) {
	if !testing.Testing() {
		panic("selfupdate: TrustKeysForTest called outside a test")
	}
	old := releaseKeys
	releaseKeys = keys
	return func() { releaseKeys = old }
}
