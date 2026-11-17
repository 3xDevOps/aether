package sshd

import (
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

type localAddrMeta struct{ ssh.ConnMetadata }

func (localAddrMeta) LocalAddr() net.Addr { return &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 2222} }

// A client that names no edge dials the hosted edge's new address, which
// reaches a server enrolled at either of its addresses, so the first-admin
// banner names --edge only for another edge.
func TestFirstAdminBannerNamesTheEdgeOnlyOffTheHostedOne(t *testing.T) {
	for origin, want := range map[string]string{
		"https://edge.cloudfield.ai": "run `aether login`, then `aether link --claim <code>`.",
		"https://edge.onaether.dev":  "run `aether login`, then `aether link --claim <code>`.",
		"https://edge.example":       "run `aether login --edge https://edge.example`, then `aether link --claim <code> --edge https://edge.example`.",
	} {
		if got := firstAdminBanner(origin, localAddrMeta{}); !strings.Contains(got, want) {
			t.Errorf("banner for a server at %s lacks %q:\n%s", origin, want, got)
		}
	}
}
