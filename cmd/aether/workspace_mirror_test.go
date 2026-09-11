package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestWorkspaceMirrorUsage(t *testing.T) {
	if err := workspaceMirror(nil); err == nil || err.Error() != workspaceMirrorUsage {
		t.Fatalf("workspaceMirror(nil) = %v, want %q", err, workspaceMirrorUsage)
	}
	if err := workspaceMirror([]string{"unknown"}); err == nil || !strings.Contains(err.Error(), "unknown workspace mirror command") {
		t.Fatalf("unknown mirror command error = %v", err)
	}
}

func TestParseWorkspaceMirrorConfigureArgs(t *testing.T) {
	knownHostsFile := filepath.Join(t.TempDir(), "known_hosts")
	const knownHosts = "example.test ssh-ed25519 AAAAhost\n"
	if err := os.WriteFile(knownHostsFile, []byte(knownHosts), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		auth string
	}{
		{name: "public", args: []string{"--source", "https://example.test/repo"}, auth: "public"},
		{name: "deploy key with known hosts", args: []string{"--workspace", "app", "--source", "ssh://git@example.test/acme/repo", "--branch", "trunk", "--auth", "deploy-key", "--known-hosts-file", knownHostsFile}, auth: "deploy-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseWorkspaceMirrorConfigureArgs(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if opts.auth != tc.auth {
				t.Fatalf("auth = %q, want %q", opts.auth, tc.auth)
			}
			if opts.source == "" {
				t.Fatal("source was not retained")
			}
			if tc.auth == "deploy-key" && opts.knownHostsFile != knownHostsFile {
				t.Fatalf("known hosts file = %q, want %q", opts.knownHostsFile, knownHostsFile)
			}
		})
	}
	if _, err := parseWorkspaceMirrorConfigureArgs([]string{"--source", "x", "--auth", "private-key"}); err == nil {
		t.Fatal("invalid auth accepted")
	}
	if _, err := parseWorkspaceMirrorConfigureArgs(nil); err == nil || !strings.Contains(err.Error(), "--source is required") {
		t.Fatalf("missing source error = %v", err)
	}
	if _, err := readMirrorKnownHosts(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing known-hosts file accepted")
	}
	got, err := readMirrorKnownHosts(knownHostsFile)
	if err != nil || got != knownHosts {
		t.Fatalf("readMirrorKnownHosts = (%q, %v), want (%q, nil)", got, err, knownHosts)
	}
}

func TestParseWorkspaceMirrorGuards(t *testing.T) {
	if _, err := parseWorkspaceMirrorRefreshArgs(nil); err == nil || !strings.Contains(err.Error(), "--workspace is required") {
		t.Fatalf("refresh without workspace = %v", err)
	}
	if _, err := parseWorkspaceMirrorRefreshArgs([]string{"--workspace", "app", "--yes"}); err == nil {
		t.Fatal("refresh accepted destructive confirmation flag")
	}
	if _, err := parseWorkspaceMirrorAdoptArgs([]string{"--workspace", "app", "--generation", "7"}); err == nil || !strings.Contains(err.Error(), "--yes is required") {
		t.Fatalf("adopt without confirmation = %v", err)
	}
	adopt, err := parseWorkspaceMirrorAdoptArgs([]string{"--workspace", "app", "--generation", "7", "--yes"})
	if err != nil || adopt.generation != 7 || !adopt.yes {
		t.Fatalf("adopt options = %+v, %v", adopt, err)
	}
	privateHosts := filepath.Join(t.TempDir(), "private")
	if writeErr := os.WriteFile(privateHosts, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nsecret\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, knownHostsErr := readMirrorKnownHosts(privateHosts); knownHostsErr == nil || !strings.Contains(knownHostsErr.Error(), "private key") {
		t.Fatalf("private-key known_hosts input error = %v", knownHostsErr)
	}
	if _, zeroGenerationErr := parseWorkspaceMirrorAdoptArgs([]string{"--workspace", "app", "--generation", "0", "--yes"}); zeroGenerationErr == nil {
		t.Fatal("zero generation accepted")
	}
	if _, disableGuardErr := parseWorkspaceMirrorDisableArgs([]string{"--workspace", "app"}); disableGuardErr == nil || !strings.Contains(disableGuardErr.Error(), "--yes is required") {
		t.Fatalf("disable without confirmation = %v", disableGuardErr)
	}
	disable, err := parseWorkspaceMirrorDisableArgs([]string{"--workspace", "app", "--yes"})
	if err != nil || disable.workspace != "app" || !disable.yes {
		t.Fatalf("disable options = %+v, %v", disable, err)
	}
}

func TestPrintWorkspaceMirrorStates(t *testing.T) {
	ws := protocol.Workspace{ID: "ws_1", Name: "app"}
	var local strings.Builder
	if err := printWorkspaceMirror(&local, ws, protocol.WorkspaceMirrorResult{}); err != nil {
		t.Fatal(err)
	}
	if got := local.String(); !strings.Contains(got, "mirror local-only") {
		t.Fatalf("local-only output = %q", got)
	}

	check := "2026-09-11T10:00:00Z"
	result := protocol.WorkspaceMirrorResult{
		Enabled:        true,
		SourceURL:      "https://github.com/acme/app.git",
		SourceIdentity: "github.com/acme/app",
		Branch:         "main",
		Auth:           "deploy-key",
		Generation:     4,
		Status:         "ready",
		ObservedCommit: strings.Repeat("a", 40),
		AcceptedCommit: strings.Repeat("b", 40),
		LastAttemptAt:  &check,
		PublicKey:      "ssh-ed25519 AAAA-public",
	}
	var full strings.Builder
	if err := printWorkspaceMirror(&full, ws, result); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"source https://github.com/acme/app.git", "branch main", "accepted SHA " + strings.Repeat("b", 40), "last check " + check, "public deploy key ssh-ed25519 AAAA-public"} {
		if !strings.Contains(full.String(), want) {
			t.Errorf("full output %q does not contain %q", full.String(), want)
		}
	}
	if strings.Contains(full.String(), "PRIVATE KEY") || strings.Contains(full.String(), "private-key") {
		t.Fatalf("mirror output leaked private material: %q", full.String())
	}
}

func TestMirrorDeployKeyInstructionsAndGitHubURL(t *testing.T) {
	result := protocol.WorkspaceMirrorResult{PublicKey: "ssh-ed25519 AAAA-public", SourceURL: "ssh://git@github.com/acme/app.git"}
	var out strings.Builder
	if err := printMirrorDeployKeyInstructions(&out, result); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"https://github.com/acme/app/settings/keys/new",
		"Settings > Deploy keys > Add deploy key",
		"Allow write access disabled",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("instructions %q do not contain %q", got, want)
		}
	}
	if got := githubDeployKeyURL("https://gitlab.com/acme/app.git"); got != "" {
		t.Fatalf("non-GitHub URL = %q, want empty", got)
	}
	if got := githubDeployKeyURL("git@github.com:acme/app.git"); got != "https://github.com/acme/app/settings/keys/new" {
		t.Fatalf("scp-like GitHub URL = %q", got)
	}
}

func TestMirrorDisableWarning(t *testing.T) {
	var out strings.Builder
	if err := printMirrorWarning(&out, "remote deploy-key revocation remains external"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "external") || !strings.Contains(out.String(), "revocation") {
		t.Fatalf("warning = %q", out.String())
	}
}
