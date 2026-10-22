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

// parseClientBanner reads the client version from an SSH banner, reporting
// legacy for a banner that is not an Aether client's.
func parseClientBanner(banner string) (clientVersion string, legacy bool) {
	v, ok := strings.CutPrefix(banner, aetherBannerPrefix)
	if !ok {
		return "", true
	}
	return strings.TrimSpace(v), false
}

// minClientVersion is the oldest client release this server serves: its
// release core, so a server built past a tag asks for that tag and never
// for a client release that does not exist.
func (s *Server) minClientVersion() string { return version.ReleaseCore(s.cfg.release) }

// upgradeRequired is the refusal for the connection behind ctx when its
// client is older than this server's release. The in-process client
// (Local) carries no client version and is never gated.
func (s *Server) upgradeRequired(ctx context.Context) *protocol.Error {
	id, ok := ctx.Value(connIdentityKey{}).(connIdentity)
	if !ok {
		return nil
	}
	client := id.clientVersion
	if id.legacyClient {
		client = legacyClientVersion
	}
	floor := s.minClientVersion()
	if !selfupdate.Behind(client, floor) {
		return nil
	}
	return protocol.UpgradeRequired(id.clientVersion, s.cfg.release, floor)
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
