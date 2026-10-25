// Package edgeproto is the wire contract between Aether servers, clients
// and an edge: identifiers, domain-separated signatures, grants,
// control-channel messages, the HTTP API and limits. docs/edge.md describes
// the edge; this package holds only what both sides must agree on.
package edgeproto

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

// Version is the protocol version this build speaks. MinVersion is the
// oldest version it still accepts. VersionHTTP is the first version whose
// servers take KindHTTP opens: a server at an earlier version refuses an
// open of a kind it does not know by dropping its control channel.
// VersionUniversalMCP is the first version whose edges route the
// universal connector paths and whose servers serve them.
const (
	Version             = 3
	MinVersion          = 1
	VersionHTTP         = 2
	VersionUniversalMCP = 3
)

// CheckVersion refuses a peer older than MinVersion with
// "upgrade required: <MinVersion>". A newer peer is accepted: it must speak
// the version the other side announced.
func CheckVersion(v int) error {
	if v < MinVersion {
		return fmt.Errorf("upgrade required: %d", MinVersion)
	}
	return nil
}

// Text bounds. Every human-facing string on the wire is bounded and free of
// control characters, because it ends up in terminals and logs.
const (
	maxShortText = 256
	maxEmail     = 320
	maxErrorText = 1024
	maxIDText    = 64
)

func validText(s string, maxLen int) bool {
	if len(s) > maxLen || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validRequiredText(s string, maxLen int) bool {
	return s != "" && validText(s, maxLen)
}
