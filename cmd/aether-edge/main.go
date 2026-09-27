// Command aether-edge is an edge: the sign-in service and the relay that
// Aether servers and clients both dial out to. docs/edge.md describes
// running one.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"github.com/3xDevOps/Aether/internal/edge"
	"github.com/3xDevOps/Aether/internal/edge/relay"
	"github.com/3xDevOps/Aether/internal/version"
)

// shutdownTimeout bounds a clean stop: drain to every server, then the
// HTTP servers.
const shutdownTimeout = 10 * time.Second

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		fmt.Println("aether-edge", version.String())
	case "serve":
		exit(serve(os.Args[2:]))
	case "servers":
		exit(servers(os.Args[2:], os.Getenv, os.Stdout))
	case "accounts":
		exit(accounts(os.Args[2:], os.Getenv, os.Stdout))
	default:
		_, _ = fmt.Fprintf(os.Stderr, "aether-edge: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func exit(err error) {
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
	case errors.Is(err, errUsage):
		os.Exit(2)
	default:
		_, _ = fmt.Fprintln(os.Stderr, "aether-edge:", err)
		os.Exit(1)
	}
}

func usage() {
	_, _ = fmt.Fprint(os.Stderr, `usage: aether-edge <command>

commands:
  serve     run the edge (aether-edge serve -h lists its options)
  servers   list, remove, block and unblock servers
  accounts  list, block, unblock and delete accounts
  version   print the version
`)
}

func serve(args []string) error {
	o, err := parseOptions(args, os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc, err := edge.New(edge.Config{
		DataDir: o.dataDir, Origin: o.origin, ServerDomain: o.serverDomain,
		GitHub: o.github, Google: o.google,
	})
	if err != nil {
		return err
	}
	defer svc.Close() //nolint:errcheck // the serve error takes precedence
	rl, err := relay.New(ctx, svc.RelayConfig(o.egressBudget))
	if err != nil {
		return err
	}
	svc.SetLink(rl)

	mux := http.NewServeMux()
	rl.Register(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/", svc.Handler())
	// No read or write timeout: relayed streams are long-lived WebSockets.
	web := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn),
	}
	metrics := &http.Server{Handler: metricsHandler(rl), ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 3)
	metricsLn, err := net.Listen("tcp", o.metricsListen)
	if err != nil {
		return fmt.Errorf("metrics listener: %w", err)
	}
	go func() { errc <- serveHTTP("metrics", metrics, metricsLn) }()

	var public net.Listener
	if o.devListen != "" {
		var ln net.Listener
		if ln, err = net.Listen("tcp", o.devListen); err != nil {
			return fmt.Errorf("development listener: %w", err)
		}
		slog.Warn("aether-edge: development mode: plain HTTP, no certificates, no browser passthrough", "listen", ln.Addr())
		go func() { errc <- serveHTTP("edge", web, ln) }()
	} else {
		var tlsConfig *tls.Config
		if tlsConfig, err = edgeTLS(o); err != nil {
			return err
		}
		if public, err = net.Listen("tcp", o.listen); err != nil {
			return fmt.Errorf("public listener: %w", err)
		}
		go func() { errc <- rl.Serve(public) }()
		go func() { errc <- serveHTTP("edge", web, tls.NewListener(rl.Listener(), tlsConfig)) }()
	}
	slog.Info("aether-edge: serving", "origin", o.origin, "server_domain", o.serverDomain, "metrics", metricsLn.Addr())

	select {
	case <-ctx.Done():
		err = nil
	case err = <-errc:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	// Drain first: servers hear the edge is stopping and reconnect with
	// backoff instead of reading a dropped socket as an outage.
	err = errors.Join(err, rl.Shutdown(shutdownCtx))
	if public != nil {
		err = errors.Join(err, ignoreClosed(public.Close()))
	}
	return errors.Join(err, web.Shutdown(shutdownCtx), metrics.Shutdown(shutdownCtx))
}

// edgeTLS obtains the certificate for exactly the edge's own host name
// with TLS-ALPN-01, which the SNI router hands to this listener like any
// other connection for that name.
func edgeTLS(o options) (*tls.Config, error) {
	u, err := url.Parse(o.origin)
	if err != nil {
		return nil, fmt.Errorf("--origin: %w", err)
	}
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(filepath.Join(o.dataDir, "acme")),
		HostPolicy: autocert.HostWhitelist(u.Hostname()),
		Email:      o.acmeEmail,
		Client:     &acme.Client{DirectoryURL: o.acmeDirectory},
	}
	cfg := m.TLSConfig()
	cfg.MinVersion = tls.VersionTLS12
	return cfg, nil
}

func serveHTTP(name string, srv *http.Server, ln net.Listener) error {
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return fmt.Errorf("%s server: %w", name, err)
}

func ignoreClosed(err error) error {
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
