package store

import (
	"crypto/rand"
	"fmt"
)

// crockford is the Crockford base32 alphabet (lowercased): case-insensitive,
// no ambiguous characters.
const crockford = "0123456789abcdefghjkmnpqrstvwxyz"

// randomIDLen is 50 bits of crypto/rand. A collision is not overwritten: IDs
// are primary keys, so the insert fails.
const randomIDLen = 10

// newID returns "<kind>-<random>", such as run-7k2m9q4xbd. IDs carry no
// time: order rows by created_at, never by id. Rows from before this format
// keep their 26-character time-first IDs.
func newID(kind string) (string, error) {
	var b [randomIDLen]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("store: generate %s id: %w", kind, err)
	}
	for i := range b {
		// 256 is a multiple of 32, so the low five bits stay uniform.
		b[i] = crockford[b[i]&0x1f]
	}
	return kind + "-" + string(b[:]), nil
}
