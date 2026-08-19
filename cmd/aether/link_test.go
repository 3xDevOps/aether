package main

import (
	"net"
	"testing"
)

// normalizeAddr must always hand cli.Dial a dialable host:port. Every
// form a user can type reaches the default SSH port unless it already
// names one, in which case it passes through untouched.
func TestNormalizeAddr(t *testing.T) {
	cases := []struct {
		name     string
		in, want string
	}{
		{"bare magicdns name", "myserver", "myserver:2222"},
		{"name with default port", "myserver:2222", "myserver:2222"},
		{"name with custom port", "myserver:22", "myserver:22"},
		{"fqdn", "box.tail1234.ts.net", "box.tail1234.ts.net:2222"},
		{"fqdn with port", "box.tail1234.ts.net:2222", "box.tail1234.ts.net:2222"},
		{"bare ipv4", "100.64.0.1", "100.64.0.1:2222"},
		{"ipv4 with port", "100.64.0.1:2222", "100.64.0.1:2222"},
		{"bare ipv6", "::1", "[::1]:2222"},
		{"bracketed ipv6", "[::1]", "[::1]:2222"},
		{"bracketed ipv6 with port", "[::1]:2222", "[::1]:2222"},
		{"bare ipv6 full", "fd7a:115c:a1e0::1", "[fd7a:115c:a1e0::1]:2222"},
		{"bracketed ipv6 with custom port", "[fd7a:115c:a1e0::1]:22", "[fd7a:115c:a1e0::1]:22"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeAddr(tc.in)
			if got != tc.want {
				t.Fatalf("normalizeAddr(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if got == "" {
				return
			}
			// The whole point of normalizing: the result must be
			// splittable into a host and a non-empty port, which is
			// what the TCP dialer requires.
			host, port, err := net.SplitHostPort(got)
			if err != nil {
				t.Fatalf("normalizeAddr(%q) = %q is not a dialable host:port: %v", tc.in, got, err)
			}
			if host == "" || port == "" {
				t.Fatalf("normalizeAddr(%q) = %q has empty host or port", tc.in, got)
			}
		})
	}
}

// Normalizing is idempotent: re-linking with a stored address must not
// stack ports or brackets.
func TestNormalizeAddrIsIdempotent(t *testing.T) {
	for _, in := range []string{"myserver", "box.tail1234.ts.net", "100.64.0.1", "::1", "[::1]", "[::1]:22"} {
		once := normalizeAddr(in)
		if twice := normalizeAddr(once); twice != once {
			t.Errorf("normalizeAddr(normalizeAddr(%q)) = %q, want %q", in, twice, once)
		}
	}
}
