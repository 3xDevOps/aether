package main

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseNameservers(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"two servers", "nameserver 8.8.8.8\nnameserver 1.1.1.1\n", []string{"8.8.8.8", "1.1.1.1"}},
		{"comments and directives", "# nameserver 9.9.9.9\nsearch lan\noptions ndots:2\n\n  nameserver   10.0.0.1  \n", []string{"10.0.0.1"}},
		{"ipv6", "nameserver 2001:4860:4860::8888\n", []string{"2001:4860:4860::8888"}},
		{"ipv6 zone", "nameserver fe80::1%wlan0\n", []string{"fe80::1%wlan0"}},
		{"not an address", "nameserver dns.example\nnameserver\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, addr := range parseNameservers([]byte(tt.in)) {
				got = append(got, addr.String())
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("parseNameservers(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// dnsResponder answers every A query with 192.0.2.7 and returns its port.
func dnsResponder(t *testing.T) uint16 {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			// Keep the header and question; drop the EDNS record Go appends.
			end := 12
			for end < n && buf[end] != 0 {
				end += int(buf[end]) + 1
			}
			end += 5
			resp := append([]byte(nil), buf[:end]...)
			resp[2], resp[3] = 0x81, 0x80
			resp[6], resp[7], resp[10], resp[11] = 0, 1, 0, 0
			resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 7)
			_, _ = pc.WriteTo(resp, addr)
		}
	}()
	return uint16(pc.LocalAddr().(*net.UDPAddr).Port)
}

func TestFileResolverReadsNameserversAtEachDial(t *testing.T) {
	port := dnsResponder(t)
	path := filepath.Join(t.TempDir(), "resolv.conf")
	resolver := newFileResolver(path, port)

	_, err := resolver.LookupIP(t.Context(), "ip4", "app.example.")
	if err == nil || !strings.Contains(err.Error(), resolvConfEnv+" "+path+": ") {
		t.Fatalf("lookup without the file: err = %v, want it to name %s %s", err, resolvConfEnv, path)
	}

	if err = os.WriteFile(path, []byte("nameserver 127.0.0.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ips, err := resolver.LookupIP(t.Context(), "ip4", "app.example.")
	if err != nil || len(ips) != 1 || ips[0].String() != "192.0.2.7" {
		t.Fatalf("lookup = %v, %v, want 192.0.2.7", ips, err)
	}

	if err := os.WriteFile(path, []byte("# nothing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.LookupIP(t.Context(), "ip4", "other.example."); err == nil || !strings.Contains(err.Error(), "no nameserver line") {
		t.Fatalf("lookup with an empty file: err = %v, want no nameserver line", err)
	}
}
