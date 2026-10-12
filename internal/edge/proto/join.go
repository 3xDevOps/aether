package edgeproto

import (
	"crypto/rand"
	"fmt"
	"net/url"
	"strings"
)

// A join secret is 16 lowercase base32 characters (80 bits) from
// crypto/rand, bound to one invitation. It travels only in the fragment of
// the invitation's link and inside SSH, so the edge never sees it.
const joinSecretLength = 16

// maxInvitationIDLength bounds an invitation id in a link or user name.
const maxInvitationIDLength = 64

// NewJoinSecret returns a fresh join secret.
func NewJoinSecret() string {
	var secret [joinSecretLength * 5 / 8]byte
	_, _ = rand.Read(secret[:]) // crypto/rand.Read never returns an error.
	return strings.ToLower(base32NoPad.EncodeToString(secret[:]))
}

func validJoinSecret(s string) bool { return len(s) == joinSecretLength && isLowerBase32(s) }

func validInvitationID(id string) bool {
	if id == "" || len(id) > maxInvitationIDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// JoinLink is the link an invitation is sent as:
// <edge origin>/join/<server id>/<invitation id>#<secret>.
func JoinLink(edgeURL, serverID, invitationID, secret string) string {
	return strings.TrimSuffix(edgeURL, "/") + "/join/" + serverID + "/" + invitationID + "#" + secret
}

// ParseJoinLink reads a link from JoinLink, or the deep link the edge's
// join page opens, aether://join?edge=<origin>&server=<id>&invitation=<id>#<secret>,
// and returns the edge's origin with the parts it names.
func ParseJoinLink(s string) (edgeURL, serverID, invitationID, secret string, err error) {
	bad := func() (string, string, string, string, error) {
		return "", "", "", "", fmt.Errorf("edgeproto: a join link looks like https://<edge host>/join/<server id>/<invitation id>#<%d-character secret>",
			joinSecretLength)
	}
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.User != nil {
		return bad()
	}
	switch u.Scheme {
	case "aether":
		q := u.Query()
		if u.Host != "join" || (u.Path != "" && u.Path != "/") || len(q) != 3 {
			return bad()
		}
		edgeURL, serverID, invitationID = q.Get("edge"), q.Get("server"), q.Get("invitation")
	case "https", "http":
		parts := strings.Split(u.EscapedPath(), "/")
		if len(parts) != 4 || parts[0] != "" || parts[1] != "join" || u.RawQuery != "" || u.ForceQuery {
			return bad()
		}
		edgeURL, serverID, invitationID = u.Scheme+"://"+u.Host, parts[2], parts[3]
	default:
		return bad()
	}
	if edgeURL, err = Origin(edgeURL); err != nil {
		return bad()
	}
	secret = u.Fragment
	if !ValidServerID(serverID) || !validInvitationID(invitationID) || !validJoinSecret(secret) {
		return bad()
	}
	return edgeURL, serverID, invitationID, secret, nil
}

// joinUserPrefix starts the SSH user name of a join connection. As with
// claimUserPrefix, the user name is sent after the client checked the host
// key against the server id in the link, so the edge relaying the
// connection never sees the secret.
const joinUserPrefix = "join:"

// JoinUser returns the SSH user name that presents the join secret of an
// invitation on a connection by owner, the account the client is signed in
// as.
func JoinUser(invitationID, secret string, owner Principal) (string, error) {
	if !validInvitationID(invitationID) || !validJoinSecret(secret) {
		return "", fmt.Errorf("edgeproto: malformed invitation id or join secret")
	}
	if owner.Type != PrincipalAccount {
		return "", fmt.Errorf("edgeproto: a join is made by an account, not a %q", owner.Type)
	}
	if err := owner.Validate(); err != nil {
		return "", err
	}
	if strings.Contains(owner.Provider, ":") {
		return "", fmt.Errorf("edgeproto: provider %q has a colon", owner.Provider)
	}
	return joinUserPrefix + invitationID + ":" + secret + ":" + owner.Provider + ":" + owner.Subject, nil
}

// ParseJoinUser returns the invitation id, join secret and joining account
// in an SSH user name, accepting only the exact form JoinUser produces. The
// secret is a secret: never log the user name of a join connection.
func ParseJoinUser(user string) (invitationID, secret string, owner Principal, ok bool) {
	rest, found := strings.CutPrefix(user, joinUserPrefix)
	if !found {
		return "", "", Principal{}, false
	}
	invitationID, rest, found = strings.Cut(rest, ":")
	if !found || !validInvitationID(invitationID) {
		return "", "", Principal{}, false
	}
	secret, rest, found = strings.Cut(rest, ":")
	if !found || !validJoinSecret(secret) {
		return "", "", Principal{}, false
	}
	provider, subject, found := strings.Cut(rest, ":")
	owner = Principal{Type: PrincipalAccount, Provider: provider, Subject: subject}
	if !found || owner.Validate() != nil {
		return "", "", Principal{}, false
	}
	return invitationID, secret, owner, true
}
