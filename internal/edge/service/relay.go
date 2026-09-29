package edge

import (
	"context"
	"net/netip"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/edge/relay"
)

// RelayConfig is the configuration of the relay that serves beside s: the
// relay origin and signing key, with s as the relay's directory and egress
// store. budget is the monthly egress budget in bytes, 0 for none.
func (s *Service) RelayConfig(budget int64) relay.Config {
	d := relayDirectory{s}
	return relay.Config{
		Origin:       s.relayOrigin,
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
	return c.Account.Account, c.Device, err
}

// Admit and AdmitClaim record each admission, so that deleting the
// account reaches every server that may hold its identity.
func (d relayDirectory) Admit(ctx context.Context, serverID string, account edgeproto.Account) error {
	if _, err := d.s.Admit(ctx, serverID, account); err != nil {
		return err
	}
	return d.s.store.RecordReach(ctx, serverID, account)
}

func (d relayDirectory) AdmitClaim(ctx context.Context, serverID string, account edgeproto.Account, addr netip.Addr) error {
	if err := d.s.AdmitClaim(ctx, serverID, account, addr); err != nil {
		return err
	}
	return d.s.store.RecordReach(ctx, serverID, account)
}

func (d relayDirectory) Enroll(ctx context.Context, serverID, name string, policy edgeproto.AccessPolicy) (bool, error) {
	state, err := d.s.ServerConnected(ctx, serverID, name, policy)
	return state == edgeproto.StateClaimed, err
}

func (d relayDirectory) ReplaceDirectory(ctx context.Context, serverID string, entries []edgeproto.DirectoryEntry) error {
	return d.s.ReplaceDirectory(ctx, serverID, edgeproto.Directory{Entries: entries})
}

func (d relayDirectory) Unenroll(ctx context.Context, serverID string) error {
	return d.s.RemoveServer(ctx, serverID)
}

func (d relayDirectory) RecordClaim(ctx context.Context, serverID, name string, policy edgeproto.AccessPolicy, owner edgeproto.Account) error {
	return d.s.RecordClaim(ctx, serverID, name, policy, owner)
}

func (d relayDirectory) TransferOwner(ctx context.Context, serverID string, owner edgeproto.Principal) error {
	return d.s.TransferOwner(ctx, serverID, owner)
}

func (d relayDirectory) DropOwner(ctx context.Context, serverID string) error {
	return d.s.DropOwner(ctx, serverID)
}

func (d relayDirectory) PendingDeletions(ctx context.Context, serverID string) ([]edgeproto.AccountDeleted, error) {
	return d.s.store.PendingDeletions(ctx, serverID)
}

func (d relayDirectory) DeletionDelivered(ctx context.Context, serverID string, del edgeproto.AccountDeleted) error {
	return d.s.store.DeletionDelivered(ctx, serverID, del)
}

func (d relayDirectory) Egress(ctx context.Context, month string) (int64, error) {
	return d.s.store.Egress(ctx, month)
}

func (d relayDirectory) AddEgress(ctx context.Context, month string, n int64) error {
	return d.s.store.AddEgress(ctx, month, n)
}
