package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestDevelopmentModeIsLoopbackOnly(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "localhost:8080", "192.0.2.1:8080", "[::]:8080"} {
		_, err := parseOptions([]string{"--dev-listen", addr, "--origin", "http://127.0.0.1:8080"}, envOf(nil))
		if err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Errorf("--dev-listen %s: %v, want a loopback refusal", addr, err)
		}
	}
	for _, addr := range []string{"127.0.0.1:8080", "[::1]:8080"} {
		if _, err := parseOptions([]string{"--dev-listen", addr, "--origin", "http://127.0.0.1:8080"}, envOf(nil)); err != nil {
			t.Errorf("--dev-listen %s: %v", addr, err)
		}
	}
	if _, err := parseOptions([]string{"--metrics-listen", "0.0.0.0:9464", "--origin", "https://edge.example.test"}, envOf(nil)); err == nil {
		t.Error("metrics on a public address accepted")
	}
	if _, err := parseOptions([]string{"--origin", "http://127.0.0.1:8080"}, envOf(nil)); err == nil {
		t.Error("plain HTTP origin accepted outside development mode")
	}
}

func TestClientSecrets(t *testing.T) {
	file := filepath.Join(t.TempDir(), "github-client-secret")
	if err := os.WriteFile(file, []byte("fake-secret-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"--origin", "https://edge.example.test", "--github-client-id", "fake-id"}

	o, err := parseOptions(append(base, "--github-client-secret-file", file), envOf(nil))
	if err != nil || o.github == nil || o.github.ClientSecret != "fake-secret-from-file" || o.google != nil {
		t.Fatalf("secret from file: %+v %v", o.github, err)
	}
	o, err = parseOptions(base, envOf(map[string]string{"AETHER_EDGE_GITHUB_CLIENT_SECRET": "fake-secret-from-env"}))
	if err != nil || o.github.ClientSecret != "fake-secret-from-env" {
		t.Fatalf("secret from the environment: %+v %v", o.github, err)
	}
	if _, err := parseOptions(base, envOf(nil)); err == nil {
		t.Error("client id without a secret accepted")
	}
	if _, err := parseOptions(append(base, "--github-client-secret-file", file),
		envOf(map[string]string{"AETHER_EDGE_GITHUB_CLIENT_SECRET": "fake"})); err == nil {
		t.Error("secret from both the environment and a file accepted")
	}
	// A secret is never a flag: its value would show in the process list.
	if _, err := parseOptions(append(base, "--github-client-secret", "fake"), envOf(nil)); err == nil {
		t.Error("--github-client-secret accepted")
	}
}
