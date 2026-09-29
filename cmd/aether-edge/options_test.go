package main

import (
	"fmt"
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
	if err != nil || o.github == nil || o.github.ClientSecret != "fake-secret-from-file" {
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

// TestSecretUnderTheUnit starts as packaging/systemd/aether-edge.service
// does: the secret variable names a credential, which SetCredential=
// leaves a single newline when /etc/aether-edge/github-client-secret is
// missing.
func TestSecretUnderTheUnit(t *testing.T) {
	github := filepath.Join(t.TempDir(), "github-client-secret")
	if err := os.WriteFile(github, []byte("fake-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"AETHER_EDGE_SIGNIN_ORIGIN":             "https://auth.example.test",
		"AETHER_EDGE_RELAY_ORIGIN":              "https://edge.example.test",
		"AETHER_EDGE_GITHUB_CLIENT_ID":          "fake-id",
		"AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE": github,
	}
	o, err := parseOptions(nil, envOf(env))
	if err != nil || o.github == nil || o.github.ClientSecret != "fake-secret" {
		t.Fatalf("secret from the credential: %+v, %v", o.github, err)
	}

	if err := os.WriteFile(github, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseOptions(nil, envOf(env)); err == nil || !strings.Contains(err.Error(), github+", named by --github-client-secret-file, holds no secret") {
		t.Fatalf("client id without a secret: %v", err)
	}
}

func TestTrustedProxies(t *testing.T) {
	proxy := func(listen, list string) (options, error) {
		return parseOptions(append([]string{"--proxy-listen", listen, "--trusted-proxies", list}, origins...), envOf(nil))
	}
	o, err := proxy("0.0.0.0:8443", "172.18.0.0/16, fd00:18::/64,::ffff:10.0.0.5/128,192.0.2.7/24")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"172.18.0.0/16", "fd00:18::/64", "10.0.0.5/32", "192.0.2.0/24"}
	if got := fmt.Sprint(o.trustedProxies); got != fmt.Sprint(want) {
		t.Errorf("trusted proxies %s, want %v", got, want)
	}
	for _, listen := range []string{":8443", "[::]:8443", "172.18.0.5:8443"} {
		if _, err = proxy(listen, "172.18.0.0/16"); err != nil {
			t.Errorf("--proxy-listen %s with --trusted-proxies: %v", listen, err)
		}
	}
	for list, want := range map[string]string{
		"0.0.0.0/0":                  `"0.0.0.0/0" trusts every address`,
		"172.18.0.0/16,::/0":         `"::/0" trusts every address`,
		"::ffff:0.0.0.0/96":          `"::ffff:0.0.0.0/96" trusts every address`,
		"172.18.0.5":                 `"172.18.0.5" is not a network such as 172.18.0.0/16`,
		"172.18.0.0/16,":             `"" is not a network`,
		"172.18.0.0/33":              `"172.18.0.0/33" is not a network`,
		"proxy.internal/32":          `"proxy.internal/32" is not a network`,
		"fe80::1%eth0/64":            `"fe80::1%eth0/64" is not a network`,
		"172.18.0.0/16;10.0.0.0/8":   `"172.18.0.0/16;10.0.0.0/8" is not a network`,
		"172.18.0.0/16 10.0.0.0/8/8": `is not a network`,
	} {
		if _, err = proxy("0.0.0.0:8443", list); err == nil || !strings.Contains(err.Error(), "--trusted-proxies: ") || !strings.Contains(err.Error(), want) {
			t.Errorf("--trusted-proxies %q: %v, want %q", list, err, want)
		}
	}
	if _, err = parseOptions(append([]string{"--trusted-proxies", "172.18.0.0/16"}, origins...), envOf(nil)); err == nil ||
		!strings.Contains(err.Error(), "set --proxy-listen too") {
		t.Errorf("--trusted-proxies without --proxy-listen: %v", err)
	}
	// Without the option, --proxy-listen stays loopback only, and says how
	// to trust a proxy elsewhere.
	if _, err = parseOptions(append([]string{"--proxy-listen", "0.0.0.0:8443"}, origins...), envOf(nil)); err == nil ||
		!strings.Contains(err.Error(), "must be a loopback address") || !strings.Contains(err.Error(), "--trusted-proxies") {
		t.Errorf("non-loopback --proxy-listen without --trusted-proxies: %v", err)
	}
	o, err = parseOptions(nil, envOf(map[string]string{
		"AETHER_EDGE_SIGNIN_ORIGIN": "https://auth.example.test", "AETHER_EDGE_RELAY_ORIGIN": "https://edge.example.test",
		"AETHER_EDGE_PROXY_LISTEN": ":8443", "AETHER_EDGE_TRUSTED_PROXIES": "10.0.0.0/8",
	}))
	if err != nil || fmt.Sprint(o.trustedProxies) != "[10.0.0.0/8]" {
		t.Errorf("--trusted-proxies from the environment: %v %v", o.trustedProxies, err)
	}
}

func TestSecretFileErrorsSayWhatToDo(t *testing.T) {
	dir := t.TempDir()
	base := append(slices.Clone(origins), "--github-client-id", "fake-id")
	_, err := parseOptions(append(base, "--github-client-secret-file", filepath.Join(dir, "missing")), envOf(nil))
	if err == nil || !strings.Contains(err.Error(), "--github-client-secret-file: open "+filepath.Join(dir, "missing")+": no such file or directory; put the secret there") {
		t.Errorf("missing secret file: %v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a file of any mode")
	}
	unreadable := filepath.Join(dir, "unreadable")
	if err = os.WriteFile(unreadable, []byte("fake-secret\n"), 0); err != nil {
		t.Fatal(err)
	}
	_, err = parseOptions(append(base, "--github-client-secret-file", unreadable), envOf(nil))
	if err == nil || !strings.Contains(err.Error(), "permission denied; make it readable by uid") {
		t.Errorf("unreadable secret file: %v", err)
	}
}
