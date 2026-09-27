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

func TestOriginMustNameAHost(t *testing.T) {
	// The placeholder the environment file ships with.
	_, err := parseOptions([]string{"--origin", "https://<edge-host>"}, envOf(nil))
	if err == nil || !strings.Contains(err.Error(), `host "<edge-host>" is not a DNS name or an IP address`) {
		t.Fatalf("placeholder origin: %v", err)
	}
}

// TestOneProviderUnderTheUnit starts as packaging/systemd/aether-edge.service
// does on an edge that offers GitHub only: both secret variables name a
// credential, and SetCredential= leaves Google's a single newline.
func TestOneProviderUnderTheUnit(t *testing.T) {
	dir := t.TempDir()
	github := filepath.Join(dir, "github-client-secret")
	google := filepath.Join(dir, "google-client-secret")
	if err := os.WriteFile(github, []byte("fake-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(google, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"AETHER_EDGE_ORIGIN":                    "https://edge.example.test",
		"AETHER_EDGE_GITHUB_CLIENT_ID":          "fake-id",
		"AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE": github,
		"AETHER_EDGE_GOOGLE_CLIENT_SECRET_FILE": google,
	}
	o, err := parseOptions(nil, envOf(env))
	if err != nil || o.github == nil || o.github.ClientSecret != "fake-secret" || o.google != nil {
		t.Fatalf("GitHub only: github %+v, google %+v, %v", o.github, o.google, err)
	}

	// A client id whose secret file is missing is a mistake, not a
	// provider left out.
	env["AETHER_EDGE_GOOGLE_CLIENT_ID"] = "fake-id"
	if _, err := parseOptions(nil, envOf(env)); err == nil || !strings.Contains(err.Error(), google+", named by --google-client-secret-file, holds no secret") {
		t.Fatalf("google id without a secret: %v", err)
	}
}
