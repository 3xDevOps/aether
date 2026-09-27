package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"golang.org/x/crypto/acme"

	"github.com/3xDevOps/Aether/internal/edge"
)

// options is the edge's configuration. Every option is a flag whose
// default comes from an AETHER_EDGE_* environment variable. OAuth client
// secrets are never flags, because a flag's value shows in the process
// list: each comes from an environment variable or from a file named by a
// flag, such as a systemd credential.
type options struct {
	listen        string
	devListen     string
	metricsListen string
	dataDir       string
	origin        string
	serverDomain  string
	acmeEmail     string
	acmeDirectory string
	egressBudget  int64
	github        *edge.OAuthApp
	google        *edge.OAuthApp
}

const maxSecretFileSize = 4 << 10

func parseOptions(args []string, getenv func(string) string) (options, error) {
	var o options
	fs := flag.NewFlagSet("aether-edge serve", flag.ContinueOnError)
	env := func(name, fallback string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return fallback
	}
	fs.StringVar(&o.listen, "listen", env("AETHER_EDGE_LISTEN", ":443"),
		"public TLS listener; connections are routed by SNI to the edge itself or passed through to a server")
	fs.StringVar(&o.devListen, "dev-listen", env("AETHER_EDGE_DEV_LISTEN", ""),
		"development mode: serve plain HTTP on this loopback address instead of --listen, without certificates or browser passthrough")
	fs.StringVar(&o.metricsListen, "metrics-listen", env("AETHER_EDGE_METRICS_LISTEN", "127.0.0.1:9464"),
		"loopback address that serves /metrics")
	fs.StringVar(&o.dataDir, "data", env("AETHER_EDGE_DATA", "/var/lib/aether-edge"),
		"data directory: edge.db, the signing key edge_key and the ACME cache")
	fs.StringVar(&o.origin, "origin", env("AETHER_EDGE_ORIGIN", ""),
		"public URL of this edge, https://host[:port]")
	fs.StringVar(&o.serverDomain, "server-domain", env("AETHER_EDGE_SERVER_DOMAIN", ""),
		"domain whose subdomains <server id>.<domain> point at this edge")
	fs.StringVar(&o.acmeEmail, "acme-email", env("AETHER_EDGE_ACME_EMAIL", ""),
		"contact email for the ACME account")
	fs.StringVar(&o.acmeDirectory, "acme-directory", env("AETHER_EDGE_ACME_DIRECTORY", acme.LetsEncryptURL),
		"ACME directory URL")
	budget := fs.String("egress-budget", env("AETHER_EDGE_EGRESS_BUDGET", "0"),
		"bytes the relay may send per calendar month (UTC) before it throttles every connection; 0 = no budget")
	githubID := fs.String("github-client-id", env("AETHER_EDGE_GITHUB_CLIENT_ID", ""), "GitHub OAuth app client id")
	githubSecretFile := fs.String("github-client-secret-file", env("AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE", ""),
		"file holding the GitHub client secret, instead of AETHER_EDGE_GITHUB_CLIENT_SECRET")
	googleID := fs.String("google-client-id", env("AETHER_EDGE_GOOGLE_CLIENT_ID", ""), "Google OAuth client id")
	googleSecretFile := fs.String("google-client-secret-file", env("AETHER_EDGE_GOOGLE_CLIENT_SECRET_FILE", ""),
		"file holding the Google client secret, instead of AETHER_EDGE_GOOGLE_CLIENT_SECRET")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	var err error
	if o.egressBudget, err = strconv.ParseInt(*budget, 10, 64); err != nil || o.egressBudget < 0 {
		return options{}, fmt.Errorf("--egress-budget %q is not a byte count", *budget)
	}
	if err = checkLoopback("--metrics-listen", o.metricsListen); err != nil {
		return options{}, err
	}
	if o.devListen != "" {
		if err = checkLoopback("--dev-listen", o.devListen); err != nil {
			return options{}, err
		}
		if !strings.HasPrefix(o.origin, "http://") {
			return options{}, fmt.Errorf("--dev-listen serves plain HTTP, so --origin must be http://<loopback address>:<port>, not %q", o.origin)
		}
	} else if !strings.HasPrefix(o.origin, "https://") {
		return options{}, fmt.Errorf("--origin must be https://host[:port], not %q; for a local plain-HTTP edge use --dev-listen", o.origin)
	}
	if o.github, err = oauthApp("github", *githubID, getenv("AETHER_EDGE_GITHUB_CLIENT_SECRET"), *githubSecretFile); err != nil {
		return options{}, err
	}
	if o.google, err = oauthApp("google", *googleID, getenv("AETHER_EDGE_GOOGLE_CLIENT_SECRET"), *googleSecretFile); err != nil {
		return options{}, err
	}
	return o, nil
}

// checkLoopback refuses an address that is not an IP loopback address and
// port. A host name is refused too: it may resolve elsewhere.
func checkLoopback(flagName, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s %q: %w", flagName, addr, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return fmt.Errorf("%s %q must be a loopback address such as 127.0.0.1:8080", flagName, addr)
	}
	return nil
}

// oauthApp returns the provider's OAuth application, or nil when it has
// no client id. The secret comes from env or from secretFile, not both.
func oauthApp(provider, clientID, envSecret, secretFile string) (*edge.OAuthApp, error) {
	upper := strings.ToUpper(provider)
	if envSecret != "" && secretFile != "" {
		return nil, fmt.Errorf("both AETHER_EDGE_%s_CLIENT_SECRET and --%s-client-secret-file are set; keep one", upper, provider)
	}
	secret := envSecret
	if secretFile != "" {
		var err error
		if secret, err = readSecretFile(secretFile); err != nil {
			return nil, fmt.Errorf("%s client secret: %w", provider, err)
		}
	}
	switch {
	case clientID == "" && secret == "":
		return nil, nil
	case clientID == "":
		return nil, fmt.Errorf("a %s client secret is set but --%s-client-id is not", provider, provider)
	case secret == "":
		return nil, fmt.Errorf("--%s-client-id is set but its secret is not; set AETHER_EDGE_%s_CLIENT_SECRET or --%s-client-secret-file",
			provider, upper, provider)
	}
	return &edge.OAuthApp{ClientID: clientID, ClientSecret: secret}, nil
}

func readSecretFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck // read-only
	data, err := io.ReadAll(io.LimitReader(f, maxSecretFileSize+1))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > maxSecretFileSize {
		return "", fmt.Errorf("%s is larger than %d bytes", path, maxSecretFileSize)
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return "", errors.New(path + " is empty")
	}
	return secret, nil
}
