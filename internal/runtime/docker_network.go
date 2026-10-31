package runtime

import (
	"context"
	"fmt"
	"net/netip"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// AgentNetwork is the Docker network Create starts containers on.
// Inter-container traffic is off, so they reach the host and the internet but
// not each other.
const AgentNetwork = "aether-agents"

// EgressNetwork is the Docker network of a Spec with Egress set. It is
// internal, so its containers have no route out and reach only the host,
// where the egress proxy listens on the network's gateway. Inter-container
// traffic is off as on AgentNetwork.
const EgressNetwork = "aether-egress"

const iccOption = "com.docker.network.bridge.enable_icc"

// ensureNetwork creates the named network when it is missing. It runs before
// every container create, so a network removed by `docker network prune`
// comes back on the next launch.
func (d *Docker) ensureNetwork(ctx context.Context, name string, internal bool) (network.Network, error) {
	inspected, err := d.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		_, err = d.cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{
			Driver:   "bridge",
			Internal: internal,
			Options:  map[string]string{iccOption: "false"},
			Labels:   map[string]string{labelManaged: "true"},
		})
		// A conflict is a concurrent create that took the name first.
		if err != nil && !cerrdefs.IsConflict(err) {
			return network.Network{}, fmt.Errorf("runtime: create docker network %s: %w", name, err)
		}
		inspected, err = d.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	}
	if err != nil {
		return network.Network{}, fmt.Errorf("runtime: inspect docker network %s: %w", name, err)
	}
	n := inspected.Network.Network
	return n, checkNetwork(name, n, internal)
}

// checkNetwork accepts any bridge network with inter-container traffic off,
// so an operator can create one with their own subnet or MTU.
func checkNetwork(name string, n network.Network, internal bool) error {
	switch {
	case n.Driver != "bridge" || n.Options[iccOption] != "false":
		return fmt.Errorf("runtime: docker network %s has driver %q and %s=%q, so containers on it could reach each other; run `docker network rm %s` and the next launch recreates it",
			name, n.Driver, iccOption, n.Options[iccOption], name)
	case internal && !n.Internal:
		return fmt.Errorf("runtime: docker network %s is not internal, so containers on it could connect out past the egress proxy; run `docker network rm %s` and the next launch recreates it",
			name, name)
	}
	return nil
}

// EgressGateway returns the host's address on EgressNetwork. A missing
// network is created when create is set and is ErrNotFound otherwise.
func (d *Docker) EgressGateway(ctx context.Context, create bool) (netip.Addr, error) {
	var n network.Network
	if create {
		var err error
		if n, err = d.ensureNetwork(ctx, EgressNetwork, true); err != nil {
			return netip.Addr{}, err
		}
	} else {
		inspected, err := d.cli.NetworkInspect(ctx, EgressNetwork, client.NetworkInspectOptions{})
		if cerrdefs.IsNotFound(err) {
			return netip.Addr{}, fmt.Errorf("runtime: docker network %s: %w", EgressNetwork, ErrNotFound)
		}
		if err != nil {
			return netip.Addr{}, fmt.Errorf("runtime: inspect docker network %s: %w", EgressNetwork, err)
		}
		n = inspected.Network.Network
	}
	for _, c := range n.IPAM.Config {
		if c.Gateway.Is4() {
			return c.Gateway, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("runtime: docker network %s has no IPv4 gateway", EgressNetwork)
}

func (d *Docker) EgressCreationKey(ctx context.Context, ip netip.Addr) (string, error) {
	list, err := d.cli.ContainerList(ctx, client.ContainerListOptions{
		Filters: make(client.Filters).Add("network", EgressNetwork),
	})
	if err != nil {
		return "", fmt.Errorf("runtime: list containers on %s: %w", EgressNetwork, err)
	}
	for _, c := range list.Items {
		if c.NetworkSettings == nil {
			continue
		}
		if endpoint := c.NetworkSettings.Networks[EgressNetwork]; endpoint != nil && endpoint.IPAddress == ip && c.Labels[labelCreationKey] != "" {
			return c.Labels[labelCreationKey], nil
		}
	}
	return "", fmt.Errorf("runtime: no session container has %s on %s: %w", ip, EgressNetwork, ErrNotFound)
}
