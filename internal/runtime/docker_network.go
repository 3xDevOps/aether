package runtime

import (
	"context"
	"fmt"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// AgentNetwork is the Docker network Create starts containers on.
// Inter-container traffic is off, so they reach the host and the internet but
// not each other.
const AgentNetwork = "aether-agents"

const iccOption = "com.docker.network.bridge.enable_icc"

// ensureAgentNetwork creates AgentNetwork when it is missing. It runs before
// every container create, so a network removed by `docker network prune`
// comes back on the next launch.
func (d *Docker) ensureAgentNetwork(ctx context.Context) error {
	inspected, err := d.cli.NetworkInspect(ctx, AgentNetwork, client.NetworkInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		_, err = d.cli.NetworkCreate(ctx, AgentNetwork, client.NetworkCreateOptions{
			Driver:  "bridge",
			Options: map[string]string{iccOption: "false"},
			Labels:  map[string]string{labelManaged: "true"},
		})
		switch {
		case err == nil:
			return nil
		case !cerrdefs.IsConflict(err):
			return fmt.Errorf("runtime: create docker network %s: %w", AgentNetwork, err)
		}
		// A concurrent create took the name first.
		inspected, err = d.cli.NetworkInspect(ctx, AgentNetwork, client.NetworkInspectOptions{})
	}
	if err != nil {
		return fmt.Errorf("runtime: inspect docker network %s: %w", AgentNetwork, err)
	}
	return checkAgentNetwork(inspected.Network.Network)
}

// checkAgentNetwork accepts any bridge network with inter-container traffic
// off, so an operator can create AgentNetwork with their own subnet or MTU.
func checkAgentNetwork(n network.Network) error {
	if n.Driver == "bridge" && n.Options[iccOption] == "false" {
		return nil
	}
	return fmt.Errorf("runtime: docker network %s has driver %q and %s=%q, so containers on it could reach each other; run `docker network rm %s` and the next launch recreates it",
		AgentNetwork, n.Driver, iccOption, n.Options[iccOption], AgentNetwork)
}
