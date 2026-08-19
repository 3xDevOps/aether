package store

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"time"
)

// crockford is the Crockford base32 alphabet (lowercased): sortable,
// case-insensitive, no ambiguous characters.
const crockford = "0123456789abcdefghjkmnpqrstvwxyz"

// newID returns a 26-character ULID-style identifier: 48 bits of Unix
// millisecond timestamp followed by 80 bits from crypto/rand, base32
// encoded. IDs sort lexicographically by creation time.
func newID() (string, error) {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixMilli())<<16)
	if _, err := rand.Read(b[6:]); err != nil {
		return "", fmt.Errorf("store: generate id: %w", err)
	}

	// Encode 128 bits as 26 base32 characters (msb-first, 2 leading zero bits).
	var out [26]byte
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&0x1f]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:]), nil
}
