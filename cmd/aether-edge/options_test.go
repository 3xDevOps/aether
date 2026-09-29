package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func envOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// origins are the two origins of a public edge; devOrigins of a local
// one, whose loopback hosts differ by name.
var (
	origins    = []string{"--signin-origin", "https://auth.example.test", "--relay-origin", "https://edge.example.test"}
	devOrigins = []string{"--signin-origin", "http://localhost:8080", "--relay-origin", "http://127.0.0.1:8080"}
)

func TestPlainHTTPIsLoopbackOnly(t *testing.T) {
	for _, mode := range []struct {
		flag    string
		origins []string
	}{{"--dev-listen", devOrigins}, {"--proxy-listen", origins}} {
		for _, addr := range []string{"0.0.0.0:8080", ":8080", "localhost:8080", "192.0.2.1:8080", "[::]:8080"} {
			_, err := parseOptions(append([]string{mode.flag, addr}, mode.origins...), envOf(nil))
			if err == nil || !strings.Contains(err.Error(), "loopback") {
				t.Errorf("%s %s: %v, want a loopback refusal", mode.flag, addr, err)
			}
		}
		for _, addr := range []string{"127.0.0.1:8080", "[::1]:8080"} {
			if _, err := parseOptions(append([]string{mode.flag, addr}, mode.origins...), envOf(nil)); err != nil {
				t.Errorf("%s %s: %v", mode.flag, addr, err)
			}
		}
	}
	if _, err := parseOptions(append([]string{"--metrics-listen", "0.0.0.0:9464"}, origins...), envOf(nil)); err == nil {
		t.Error("metrics on a public address accepted")
	}
	for _, args := range [][]string{
		devOrigins,
		append([]string{"--proxy-listen", "127.0.0.1:8443"}, devOrigins...),
	} {
		if _, err := parseOptions(args, envOf(nil)); err == nil || !strings.Contains(err.Error(), "must be https://host[:port]") {
			t.Errorf("%v: %v, want plain HTTP origins refused outside development mode", args, err)
		}
	}
	if _, err := parseOptions(append([]string{"--dev-listen", "127.0.0.1:8080"}, origins...), envOf(nil)); err == nil ||
		!strings.Contains(err.Error(), "--dev-listen serves plain HTTP, so --signin-origin must be http://") {
		t.Errorf("https origins in development mode: %v", err)
	}
	if _, err := parseOptions(append([]string{"--dev-listen", "127.0.0.1:8080", "--proxy-listen", "127.0.0.1:8443"}, devOrigins...), envOf(nil)); err == nil {
		t.Error("--dev-listen and --proxy-listen together accepted")
	}
}

func TestOriginsAreTwoHosts(t *testing.T) {
	for _, args := range [][]string{
		{"--signin-origin", "https://edge.example.test", "--relay-origin", "https://edge.example.test"},
		{"--signin-origin", "https://edge.example.test:8443", "--relay-origin", "https://EDGE.example.test"},
	} {
		if _, err := parseOptions(args, envOf(nil)); err == nil || !strings.Contains(err.Error(), "must name different hosts") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if _, err := parseOptions([]string{"--relay-origin", "https://edge.example.test"}, envOf(nil)); err == nil ||
		!strings.Contains(err.Error(), `--signin-origin must be https://host[:port], not ""`) {
		t.Errorf("no sign-in origin: %v", err)
	}
	o, err := parseOptions(nil, envOf(map[string]string{
		"AETHER_EDGE_SIGNIN_ORIGIN": "https://auth.example.test", "AETHER_EDGE_RELAY_ORIGIN": "https://edge.example.test",
		"AETHER_EDGE_PROXY_LISTEN": "127.0.0.1:8443",
	}))
	if err != nil || o.signinOrigin != "https://auth.example.test" || o.relayOrigin != "https://edge.example.test" || o.proxyListen != "127.0.0.1:8443" {
		t.Fatalf("options from the environment: %+v %v", o, err)
	}
}

func TestClientSecrets(t *testing.T) {
	file := filepath.Join(t.TempDir(), "github-client-secret")
	if err := os.WriteFile(file, []byte("fake-secret-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := append(slices.Clone(origins), "--github-client-id", "fake-id")

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
	// The placeholders the environment file ships with.
	_, err := parseOptions([]string{"--signin-origin", "https://<signin-host>", "--relay-origin", "https://<relay-host>"}, envOf(nil))
	if err == nil || !strings.Contains(err.Error(), `--signin-origin: edgeproto: edge url "https://<signin-host>": host "<signin-host>" is not a DNS name or an IP address`) {
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
		"AETHER_EDGE_SIGNIN_ORIGIN":             "https://auth.example.test",
		"AETHER_EDGE_RELAY_ORIGIN":              "https://edge.example.test",
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
