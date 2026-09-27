package edge

import (
	"context"

	"github.com/3xDevOps/Aether/internal/edge/relay"
	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// RelayConfig is the configuration of the relay that serves beside s: the
// same origin, server domain and signing key, with s as the relay's
// directory and egress store. budget is the monthly egress budget in
// bytes, 0 for none.
func (s *Service) RelayConfig(budget int64) relay.Config {
	d := relayDirectory{s}
	return relay.Config{
		Origin:       s.origin,
		ServerDomain: s.serverDomain,
		EdgeKey:      s.key,
		Directory:    d,
		Egress:       d,
		EgressBudget: budget,
	}
}

// relayDirectory is the Service as relay.Directory and relay.EgressStore.
type relayDirectory struct{ s *Service }

func (d relayDirectory) Authenticate(ctx context.Context, token string) (edgeproto.Account, edgeproto.Device, error) {
	c, _, err := d.s.deviceByToken(ctx, token)
	return c.Account, c.Device, err
}

func (d relayDirectory) Admit(ctx context.Context, serverID string, account edgeproto.Account) error {
	_, err := d.s.Admit(ctx, serverID, account)
	return err
}

func (d relayDirectory) Claimed(ctx context.Context, serverID, name string) (bool, error) {
	state, err := d.s.ServerConnected(ctx, serverID, name)
	return state == edgeproto.StateClaimed, err
}

func (d relayDirectory) ReplaceDirectory(ctx context.Context, serverID string, entries []edgeproto.DirectoryEntry) error {
	return d.s.ReplaceDirectory(ctx, serverID, edgeproto.Directory{Entries: entries})
}

func (d relayDirectory) RedeemWebCode(ctx context.Context, serverID string, m edgeproto.WebRedeem) (string, error) {
	res := d.s.RedeemWebCode(ctx, serverID, m)
	if res.Error != "" {
		return "", edgeproto.Refusal(res.Error)
	}
	return res.Grant, nil
}

func (d relayDirectory) Unenroll(ctx context.Context, serverID string) error {
	return d.s.RemoveServer(ctx, serverID)
}

func (d relayDirectory) Egress(ctx context.Context, month string) (int64, error) {
	return d.s.store.Egress(ctx, month)
}

func (d relayDirectory) AddEgress(ctx context.Context, month string, n int64) error {
	return d.s.store.AddEgress(ctx, month, n)
}
