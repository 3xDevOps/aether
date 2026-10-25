// Package egress is the HTTP proxy that runs of a workspace with egress
// control on reach the outside through, and the host list it enforces.
package egress

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Port is fixed so that runs which outlive a server restart find the proxy
// again.
const Port = 3128

// Defaults is a workspace's host list until an admin edits it.
// docs/environments.md says which agent or tool needs each.
var Defaults = []string{
	"api.anthropic.com", "claude.ai", "claude.com", "platform.claude.com",
	"api.openai.com", "auth.openai.com", "chatgpt.com",
	"api.githubcopilot.com", "*.individual.githubcopilot.com", "*.business.githubcopilot.com", "*.enterprise.githubcopilot.com", "copilot-proxy.githubusercontent.com",
	"generativelanguage.googleapis.com", "cloudcode-pa.googleapis.com", "accounts.google.com", "oauth2.googleapis.com", "www.googleapis.com",
	"*.cursor.sh", "cursor.com",
	"opencode.ai", "models.dev",
	"github.com", "api.github.com", "codeload.github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com", "raw.githubusercontent.com",
	"registry.npmjs.org",
	"pypi.org", "files.pythonhosted.org",
	"proxy.golang.org", "sum.golang.org", "storage.googleapis.com",
	"crates.io", "index.crates.io", "static.crates.io",
	"registry-1.docker.io", "auth.docker.io", "production.cloudflare.docker.com", "production.cloudfront.docker.com", "docker-images-prod.6aa30f8b08e16409b46e0173d6de2f56.r2.cloudflarestorage.com",
	"archive.ubuntu.com", "security.ubuntu.com", "ports.ubuntu.com",
}

func NormalizeHost(entry string) (string, error) {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(entry)), ".")
	name := strings.TrimPrefix(host, "*.")
	if len(name) > 253 || !validName(name) {
		return "", fmt.Errorf("%q is not a host name or a *.domain pattern", entry)
	}
	return host, nil
}

func validName(name string) bool {
	for label := range strings.SplitSeq(name, ".") {
		if label == "" || len(label) > 63 || strings.Trim(label, "abcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
			return false
		}
	}
	return true
}

// host must be lowercase and without a trailing dot.
func allowed(hosts []string, host string) bool {
	for _, entry := range hosts {
		domain, wildcard := strings.CutPrefix(entry, "*")
		if entry == host || wildcard && strings.HasSuffix(host, domain) {
			return true
		}
	}
	return false
}

func Hosts(policy domain.EgressPolicy) []string {
	if policy.Hosts == nil {
		return Defaults
	}
	return policy.Hosts
}

// NODE_USE_ENV_PROXY makes the fetch of Node 24 and later use the proxy.
func Env(addr netip.AddrPort) map[string]string {
	proxy := "http://" + addr.String()
	local := "localhost,127.0.0.1,::1"
	return map[string]string{
		"HTTP_PROXY": proxy, "HTTPS_PROXY": proxy, "http_proxy": proxy, "https_proxy": proxy,
		"NO_PROXY": local, "no_proxy": local,
		"NODE_USE_ENV_PROXY": "1",
	}
}
