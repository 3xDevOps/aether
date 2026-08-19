// Package reachability discovers how clients can reach this server.
// Each Adapter probes one mechanism (plain host, Tailscale MagicDNS) and
// reports the endpoint it provides. Discovery is best-effort: an adapter
// whose mechanism is not present degrades to an error and callers fall
// through to the next one.
package reachability

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// Endpoint is one way the server can be reached.
type Endpoint struct {
	// Host is the hostname or address clients dial.
	Host string
	// Port is an optional port hint; 0 means the server's configured
	// listen port applies unchanged.
	Port int
	// Label is a short human-readable source tag ("host", "tailnet").
	Label string
}

// Adapter discovers one way the server can be reached. Implementations
// probe a single mechanism and return either the endpoint it provides or
// an error meaning "not present here"; callers try adapters in order and
// take the first success (see Discover).
//
// Tunnel seam: a future tunnel adapter (cloudflared-style) plugs in by
// implementing this same interface. Name() names the tunnel provider,
// Discover starts or inspects the tunnel and returns its public hostname,
// with Port carrying the tunnel's public port when it differs from the
// server's listen port. No tunnel implementation ships in v1; this
// interface is the only contract one must meet.
type Adapter interface {
	// Name identifies the adapter ("host", "tailscale") in logs and CLI
	// output.
	Name() string
	// Discover probes the mechanism and returns its endpoint, or an
	// error when the mechanism is not present or not usable.
	Discover(ctx context.Context) (Endpoint, error)
}

// Discover tries adapters in order and returns the first endpoint one of
// them provides. It errors only when every adapter fails.
func Discover(ctx context.Context, adapters ...Adapter) (Endpoint, error) {
	var errs []error
	for _, a := range adapters {
		ep, err := a.Discover(ctx)
		if err == nil {
			return ep, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", a.Name(), err))
	}
	if len(errs) == 0 {
		return Endpoint{}, errors.New("reachability: no adapters")
	}
	return Endpoint{}, fmt.Errorf("reachability: %w", errors.Join(errs...))
}

// Host is the zero-dependency default adapter: the machine's own
// hostname, reachable wherever plain host:port routing already works.
type Host struct{}

// Name implements Adapter.
func (Host) Name() string { return "host" }

// Discover implements Adapter using the OS hostname.
func (Host) Discover(context.Context) (Endpoint, error) {
	name, err := os.Hostname()
	if err != nil {
		return Endpoint{}, fmt.Errorf("hostname: %w", err)
	}
	return Endpoint{Host: name, Label: "host"}, nil
}
