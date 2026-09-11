package mirror

import (
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestCanonicalizeSourceRoundTripsCanonicalURLs(t *testing.T) {
	const knownHosts = "git.example.test ssh-ed25519 AAAA"
	tests := []struct {
		name       string
		raw        string
		auth       domain.MirrorAuth
		knownHosts string
	}{
		{name: "public HTTPS", raw: "https://Example.test/acme/repo.git", auth: domain.MirrorAuthPublic},
		{name: "GitHub deploy HTTPS", raw: "https://github.com/acme/repo", auth: domain.MirrorAuthDeployKey},
		{name: "GitHub deploy SSH", raw: "ssh://git@github.com/acme/repo.git", auth: domain.MirrorAuthDeployKey},
		{name: "GitHub deploy SSH default user", raw: "ssh://github.com/acme/repo.git", auth: domain.MirrorAuthDeployKey},
		{name: "generic SSH", raw: "ssh://git.example.test/acme/repo.git", auth: domain.MirrorAuthDeployKey, knownHosts: knownHosts},
		{name: "generic SSH user", raw: "ssh://deploy-bot@git.example.test/acme/repo.git", auth: domain.MirrorAuthDeployKey, knownHosts: knownHosts},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			source, err := CanonicalizeSource(tc.raw, tc.auth, tc.knownHosts)
			if err != nil {
				t.Fatalf("canonicalize source: %v", err)
			}
			roundTrip, err := CanonicalizeSource(source.URL, tc.auth, tc.knownHosts)
			if err != nil {
				t.Fatalf("canonicalize canonical URL %q: %v", source.URL, err)
			}
			if roundTrip.URL != source.URL || roundTrip.Identity != source.Identity ||
				roundTrip.KnownHosts != source.KnownHosts ||
				roundTrip.GitHub != source.GitHub || roundTrip.GenericSSH != source.GenericSSH {
				t.Fatalf("canonical source round-trip = %+v, want %+v", roundTrip, source)
			}
		})
	}
}

func TestCanonicalizeSourceRejectsHTTPSCredentialsAndUnsafeSSHUsers(t *testing.T) {
	const knownHosts = "git.example.test ssh-ed25519 AAAA"
	tests := []struct {
		name       string
		raw        string
		auth       domain.MirrorAuth
		knownHosts string
	}{
		{name: "public HTTPS userinfo", raw: "https://alice@example.test/repo", auth: domain.MirrorAuthPublic},
		{name: "public HTTPS password", raw: "https://alice:secret@example.test/repo", auth: domain.MirrorAuthPublic},
		{name: "GitHub HTTPS userinfo", raw: "https://alice@github.com/acme/repo", auth: domain.MirrorAuthDeployKey},
		{name: "GitHub HTTPS password", raw: "https://alice:secret@github.com/acme/repo", auth: domain.MirrorAuthDeployKey},
		{name: "GitHub SSH user", raw: "ssh://alice@github.com/acme/repo", auth: domain.MirrorAuthDeployKey},
		{name: "SSH password", raw: "ssh://git:secret@git.example.test/repo", auth: domain.MirrorAuthDeployKey, knownHosts: knownHosts},
		{name: "SSH empty password", raw: "ssh://git:@git.example.test/repo", auth: domain.MirrorAuthDeployKey, knownHosts: knownHosts},
		{name: "SSH option user", raw: "ssh://-oProxyCommand=touch@git.example.test/repo", auth: domain.MirrorAuthDeployKey, knownHosts: knownHosts},
		{name: "SSH shell user", raw: "ssh://git%3Brm@git.example.test/repo", auth: domain.MirrorAuthDeployKey, knownHosts: knownHosts},
		{name: "SSH whitespace user", raw: "ssh://git%20name@git.example.test/repo", auth: domain.MirrorAuthDeployKey, knownHosts: knownHosts},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := CanonicalizeSource(tc.raw, tc.auth, tc.knownHosts); err == nil {
				t.Fatalf("accepted forbidden source %q", tc.raw)
			}
		})
	}
}
