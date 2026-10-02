package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"

	"golang.org/x/crypto/acme"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/edge/relay"
	edge "github.com/3xDevOps/Aether/internal/edge/service"
)

// options is the edge's configuration. Every option is a flag whose
// default comes from an AETHER_EDGE_* environment variable. The GitHub
// client secret is never a flag, because a flag's value shows in the
// process list: it comes from an environment variable or from a file named
// by a flag, such as a systemd credential.
type options struct {
	listen      string
	proxyListen string
	// trustedProxies, when set, are the networks of the proxies in front
	// of proxyListen, which may then be any address.
	trustedProxies []netip.Prefix
	devListen      string
	metricsListen  string
	dataDir        string
	signinOrigin   string
	relayOrigin    string
	acmeEmail      string
	acmeDirectory  string
	egressBudget   int64
	github         *edge.OAuthApp
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
		"public TLS listener, with certificates for both origins' hosts")
	fs.StringVar(&o.proxyListen, "proxy-listen", env("AETHER_EDGE_PROXY_LISTEN", ""),
		"behind a reverse proxy that terminates TLS: serve plain HTTP on this address instead of --listen, "+
			"and read client addresses from the proxy's "+relay.HeaderForwardedFor+" header; a loopback address unless --trusted-proxies names the proxies")
	trustedProxies := fs.String("trusted-proxies", env("AETHER_EDGE_TRUSTED_PROXIES", ""),
		"comma-separated networks of the reverse proxies in front of --proxy-listen when they are not on this host: "+
			"each proxy's own address, such as 10.0.0.5/32, or a network that holds only proxies, since every peer in them can set the client address. "+
			"--proxy-listen may then be any address, and requests from any other peer are refused")
	fs.StringVar(&o.devListen, "dev-listen", env("AETHER_EDGE_DEV_LISTEN", ""),
		"development mode: serve plain HTTP on this loopback address instead of --listen, without certificates")
	fs.StringVar(&o.metricsListen, "metrics-listen", metricsListenDefault(getenv),
		"loopback address that serves /metrics and /healthz")
	fs.StringVar(&o.dataDir, "data", dataDirDefault(getenv),
		"data directory: edge.db, the signing key edge_key and the ACME cache")
	fs.StringVar(&o.signinOrigin, "signin-origin", env("AETHER_EDGE_SIGNIN_ORIGIN", ""),
		"public URL people sign in on and clients call, https://host[:port]")
	fs.StringVar(&o.relayOrigin, "relay-origin", env("AETHER_EDGE_RELAY_ORIGIN", ""),
		"public URL servers enroll with and clients connect through, https://host[:port], on another host than --signin-origin")
	fs.StringVar(&o.acmeEmail, "acme-email", env("AETHER_EDGE_ACME_EMAIL", ""),
		"contact email for the ACME account")
	fs.StringVar(&o.acmeDirectory, "acme-directory", env("AETHER_EDGE_ACME_DIRECTORY", acme.LetsEncryptURL),
		"ACME directory URL")
	budget := fs.String("egress-budget", env("AETHER_EDGE_EGRESS_BUDGET", "0"),
		"bytes the relay may send per calendar month (UTC) before it throttles every connection; 0 = no budget")
	githubID := fs.String("github-client-id", env("AETHER_EDGE_GITHUB_CLIENT_ID", ""), "GitHub OAuth app client id")
	githubSecretFile := fs.String("github-client-secret-file", env("AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE", ""),
		"file holding the GitHub client secret, instead of AETHER_EDGE_GITHUB_CLIENT_SECRET")
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
	scheme := "https://"
	switch {
	case o.devListen != "" && o.proxyListen != "":
		return options{}, errors.New("--dev-listen and --proxy-listen are two ways to serve plain HTTP; set one")
	case *trustedProxies != "" && o.proxyListen == "":
		return options{}, errors.New("--trusted-proxies names the proxies in front of --proxy-listen; set --proxy-listen too")
	case *trustedProxies != "":
		if o.trustedProxies, err = parseTrustedProxies(*trustedProxies); err != nil {
			return options{}, err
		}
		if _, _, err = net.SplitHostPort(o.proxyListen); err != nil {
			return options{}, fmt.Errorf("--proxy-listen %q: %w", o.proxyListen, err)
		}
	case o.proxyListen != "":
		if err = checkLoopback("--proxy-listen", o.proxyListen); err != nil {
			return options{}, fmt.Errorf("%w, or name the proxies' networks with --trusted-proxies", err)
		}
	case o.devListen != "":
		if err = checkLoopback("--dev-listen", o.devListen); err != nil {
			return options{}, err
		}
		scheme = "http://"
	}
	for _, origin := range []struct{ flag, env, value string }{
		{"--signin-origin", "AETHER_EDGE_SIGNIN_ORIGIN", o.signinOrigin},
		{"--relay-origin", "AETHER_EDGE_RELAY_ORIGIN", o.relayOrigin},
	} {
		if origin.value == "" {
			return options{}, fmt.Errorf("%s must be %shost[:port], not \"\"; set it or %s", origin.flag, scheme, origin.env)
		}
		if !strings.HasPrefix(origin.value, scheme) {
			if scheme == "http://" {
				return options{}, fmt.Errorf("--dev-listen serves plain HTTP, so %s must be http://<loopback host>:<port>, not %q", origin.flag, origin.value)
			}
			return options{}, fmt.Errorf("%s must be https://host[:port], not %q; for a local plain-HTTP edge use --dev-listen", origin.flag, origin.value)
		}
		if _, err = edgeproto.Origin(origin.value); err != nil {
			return options{}, fmt.Errorf("%s: %w", origin.flag, err)
		}
	}
	if host(o.signinOrigin) == host(o.relayOrigin) {
		return options{}, fmt.Errorf("--signin-origin %s and --relay-origin %s must name different hosts, such as auth.example.com and edge.example.com",
			o.signinOrigin, o.relayOrigin)
	}
	if o.github, err = githubApp(*githubID, getenv("AETHER_EDGE_GITHUB_CLIENT_SECRET"), *githubSecretFile); err != nil {
		return options{}, err
	}
	return o, nil
}

// host is the lowercase host name of a valid origin.
func host(origin string) string {
	u, _ := url.Parse(origin) // parseOptions checked it
	return strings.ToLower(u.Hostname())
}

// dataDirDefault is the default of --data: AETHER_EDGE_DATA, else the
// unit's state directory.
func dataDirDefault(getenv func(string) string) string {
	if d := getenv("AETHER_EDGE_DATA"); d != "" {
		return d
	}
	return "/var/lib/aether-edge"
}

// parseTrustedProxies parses --trusted-proxies. An IPv4-mapped network
// is kept as the IPv4 network it maps, since peers are compared unmapped.
func parseTrustedProxies(list string) ([]netip.Prefix, error) {
	var proxies []netip.Prefix
	for field := range strings.SplitSeq(list, ",") {
		field = strings.TrimSpace(field)
		p, err := netip.ParsePrefix(field)
		if err != nil {
			return nil, fmt.Errorf("--trusted-proxies: %q is not a network such as 172.18.0.0/16 or 10.0.0.5/32", field)
		}
		if p.Addr().Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		if p.Bits() == 0 {
			return nil, fmt.Errorf("--trusted-proxies: %q trusts every address, so any client could choose the address the edge limits it by; name the proxies' own network", field)
		}
		proxies = append(proxies, p.Masked())
	}
	return proxies, nil
}

// metricsListenDefault is the default of --metrics-listen, shared with
// aether-edge healthcheck.
func metricsListenDefault(getenv func(string) string) string {
	if a := getenv("AETHER_EDGE_METRICS_LISTEN"); a != "" {
		return a
	}
	return "127.0.0.1:9464"
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

// githubApp returns the GitHub OAuth app, or nil when it has neither a
// client id nor a secret. The secret comes from env or from secretFile,
// not both. A secret file holding only white space holds no secret: the
// systemd unit gives the edge such a file when the real one is missing.
func githubApp(clientID, envSecret, secretFile string) (*edge.OAuthApp, error) {
	if envSecret != "" && secretFile != "" {
		return nil, errors.New("both AETHER_EDGE_GITHUB_CLIENT_SECRET and --github-client-secret-file are set; keep one")
	}
	secret := envSecret
	if secretFile != "" {
		var err error
		if secret, err = readSecretFile(secretFile); err != nil {
			return nil, fmt.Errorf("--github-client-secret-file: %w", err)
		}
	}
	switch {
	case clientID == "" && secret == "":
		return nil, nil
	case clientID == "":
		return nil, errors.New("a github client secret is set but --github-client-id is not")
	case secret == "" && secretFile != "":
		return nil, fmt.Errorf("--github-client-id is set but %s, named by --github-client-secret-file, holds no secret", secretFile)
	case secret == "":
		return nil, errors.New("--github-client-id is set but its secret is not; name a file holding it with AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE " +
			"(--github-client-secret-file), or set AETHER_EDGE_GITHUB_CLIENT_SECRET")
	}
	return &edge.OAuthApp{ClientID: clientID, ClientSecret: secret}, nil
}

func readSecretFile(path string) (string, error) {
	f, err := os.Open(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%w; put the secret there, or name the file that holds it", err)
	case errors.Is(err, fs.ErrPermission):
		return "", fmt.Errorf("%w; make it readable by uid %d, the user aether-edge runs as", err, os.Geteuid())
	case err != nil:
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
	return strings.TrimSpace(string(data)), nil
}
