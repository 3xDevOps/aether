package sshd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/version"
)

// aetherBannerPrefix opens the SSH banner of every Aether client:
// "SSH-2.0-Aether <version>".
const aetherBannerPrefix = "SSH-2.0-Aether "

// legacyClientVersion stands in for a client whose banner names no
// version. It sorts below every release, so any parseable server release
// gates it.
const legacyClientVersion = "v0.0.0-0"

// ungatedMethods stay callable by a client the server's release gates: a
// client learns why it is refused, and how the server's own update goes.
var ungatedMethods = map[string]bool{
	protocol.MethodServerInfo:         true,
	protocol.MethodServerUpdateStatus: true,
}

// upgradeRefusalTimeout bounds the wait for the header of a stream the
// server is about to refuse.
const upgradeRefusalTimeout = 10 * time.Second

// parseClientBanner reads the client release from an SSH banner, its
// build from the version's build metadata, reporting legacy for a banner
// that is not an Aether client's.
func parseClientBanner(banner string) (client selfupdate.Release, legacy bool) {
	v, ok := strings.CutPrefix(banner, aetherBannerPrefix)
	if !ok {
		return selfupdate.Release{}, true
	}
	return selfupdate.ParseBannerVersion(strings.TrimSpace(v)), false
}

// minClient is the oldest client release this server serves: its release
// core, so a server built past a tag asks for that tag and never for a
// client release that does not exist, at its own build.
func (s *Server) minClient() selfupdate.Release {
	return selfupdate.Release{Version: version.ReleaseCore(s.cfg.release), Build: s.cfg.build, Hotfix: s.cfg.hotfix}
}

// upgradeRequired is the refusal for the connection behind ctx when its
// client is older than this server's release, by build when both carry
// one, else by version. The in-process client (Local) carries no client
// version and is never gated.
//
// The gate keeps a client from calling a server newer than it, for the
// client's sake; it is not access control. A client that claims a higher
// build in its banner gains what claiming a higher version always gave:
// calls the server answers by role like any other.
func (s *Server) upgradeRequired(ctx context.Context) *protocol.Error {
	id, ok := ctx.Value(connIdentityKey{}).(connIdentity)
	if !ok {
		return nil
	}
	client := id.client
	if id.legacyClient {
		client = selfupdate.Release{Version: legacyClientVersion}
	}
	floor := s.minClient()
	if !selfupdate.Ahead(floor, client) {
		return nil
	}
	return protocol.UpgradeRequired(id.client.Version, s.cfg.release, floor.Version)
}

// refuseUpgrade answers a stream from a gated client in the shape every
// subsystem ack shares, then ends it with exit status 1. The message also
// goes to stderr, which is all a raw stream such as aether-sync's shows.
// The header is read first so the client's write of it cannot race the
// close.
func refuseUpgrade(ch ssh.Channel, perr *protocol.Error) {
	defer func() { _ = ch.Close() }()
	timer := time.AfterFunc(upgradeRefusalTimeout, func() { _ = ch.Close() })
	defer timer.Stop()
	_, _ = protocol.ReadLine(bufio.NewReader(&capReader{r: ch, left: maxSubsystemHeaderBytes}))
	_ = writeJSONLine(ch, struct {
		OK    bool            `json:"ok"`
		Code  int             `json:"code"`
		Error string          `json:"error"`
		Data  json.RawMessage `json:"data"`
	}{Code: perr.Code, Error: perr.Message, Data: perr.Data})
	_, _ = fmt.Fprintf(ch.Stderr(), "aether: %s\n", perr.Message)
	sendExitStatus(ch, 1)
}
