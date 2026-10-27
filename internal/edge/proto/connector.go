package edgeproto

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// The universal connector paths, on the relay origin. One URL,
// <relay origin>/mcp, is every server's connector: its MCP endpoint, OAuth
// issuer and protected resource. Servers prefix each authorization code,
// access token and refresh token they mint for it with "<server id>.", and
// the edge forwards PathUniversalMCP by the bearer token's prefix and
// PathUniversalMCPToken by the code's or refresh token's. A
// PathUniversalMCPAuthorize request without a server query parameter goes
// to PathMCPChoose on the sign-in origin, where the signed-in account
// chooses a server; its form posts the request back with ParamServer and
// a ConnectorAssertion in ParamAssertion. The edge serves the two
// metadata documents itself.
const (
	PathUniversalMCP                = "/mcp"
	PathUniversalMCPAuthorize       = "/mcp/authorize"
	PathUniversalMCPToken           = "/mcp/token"
	PathUniversalResourceMetadata   = "/.well-known/oauth-protected-resource/mcp"
	PathUniversalAuthServerMetadata = "/.well-known/oauth-authorization-server/mcp"
	// PathOpenAIChallenge serves the operator's OpenAI domain verification
	// token.
	PathOpenAIChallenge = "/.well-known/openai-apps-challenge"
)

// PathMCPChoose is served on the sign-in origin.
const PathMCPChoose = "/connectors/choose"

// Form fields the edge adds to a PathUniversalMCPAuthorize request it
// sends to a server. ParamServer may also be a query parameter;
// ParamAssertion never is, so no URL, log or Referer carries it.
const (
	ParamServer    = "server"
	ParamAssertion = "edge_assertion"
)

// HeaderBrowser carries, on each PathUniversalMCPAuthorize request the
// edge relays, BrowserBinding of the browser cookie the edge set on the
// relay origin. A client cannot set it: the edge replaces any it sends,
// and drops it on every other path.
const HeaderBrowser = "Aether-Edge-Browser"

// BrowserBinding is what HeaderBrowser carries to serverID for a browser
// holding cookie: a hash that names the browser to that server alone.
func BrowserBinding(serverID, cookie string) string {
	sum := sha256.Sum256([]byte("aether-edge-browser-v1\x00" + serverID + "\x00" + cookie))
	return hex.EncodeToString(sum[:])
}

// TokenServer returns the server id a universal code or token is prefixed
// with.
func TokenServer(token string) (string, bool) {
	id, rest, ok := strings.Cut(token, ".")
	if !ok || rest == "" || !ValidServerID(id) {
		return "", false
	}
	return id, true
}

// ConnectorAssertionTTL is the longest lifetime of a ConnectorAssertion:
// time for a person to read a consent page.
const ConnectorAssertionTTL = 5 * time.Minute

const assertionContext = "aether-edge-connector-assertion-v1\x00"

// ConnectorAssertion is the edge's signed statement that Account, signed
// in to the edge in a browser, chose ServerID for the authorization
// request of ClientID with PKCE challenge Challenge and redirect URI
// RedirectURI on the universal connector. Nonce makes it single-use: the
// server binds it to the first browser that brings it and spends it.
// Issuer is the edge's canonical relay origin. Edges before
// VersionConnectorSignIn leave RedirectURI empty.
type ConnectorAssertion struct {
	Issuer      string    `json:"issuer"`
	ServerID    string    `json:"server_id"`
	Nonce       string    `json:"nonce"`
	Account     Account   `json:"account"`
	ClientID    string    `json:"client_id"`
	Challenge   string    `json:"challenge"`
	RedirectURI string    `json:"redirect_uri,omitempty"`
	IssuedAt    time.Time `json:"issued_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Assertion verification errors, beside ErrGrantMalformed and
// ErrGrantSignature. VerifyConnectorAssertion wraps them with detail.
var (
	ErrAssertionScope   = errors.New("edge assertion is for another edge, server or authorization request")
	ErrAssertionExpired = errors.New("edge assertion expired")
)

func (a ConnectorAssertion) validate() error {
	if o, err := Origin(a.Issuer); err != nil || o != a.Issuer {
		return fmt.Errorf("issuer %q is not a canonical edge origin", a.Issuer)
	}
	switch {
	case !ValidServerID(a.ServerID):
		return fmt.Errorf("invalid server id %q", a.ServerID)
	case !ValidConnID(a.Nonce):
		return fmt.Errorf("invalid nonce %q", a.Nonce)
	case !validRequiredText(a.ClientID, maxShortText) || !validRequiredText(a.Challenge, maxShortText) ||
		!validText(a.RedirectURI, maxShortText):
		return errors.New("client id or challenge is empty, or one of them or the redirect URI is too long or has control characters")
	case a.IssuedAt.IsZero() || !a.ExpiresAt.After(a.IssuedAt):
		return errors.New("assertion must expire after it is issued")
	case a.ExpiresAt.Sub(a.IssuedAt) > ConnectorAssertionTTL:
		return fmt.Errorf("assertion lifetime %s exceeds %s", a.ExpiresAt.Sub(a.IssuedAt), ConnectorAssertionTTL)
	}
	return a.Account.Validate()
}

// SignConnectorAssertion signs a with the edge key, in the encoding of
// SignGrant under its own context.
func SignConnectorAssertion(edgeKey ed25519.PrivateKey, a ConnectorAssertion) (string, error) {
	a.IssuedAt, a.ExpiresAt = a.IssuedAt.UTC(), a.ExpiresAt.UTC()
	if err := a.validate(); err != nil {
		return "", fmt.Errorf("edgeproto: sign connector assertion: %w", err)
	}
	return signPayload(edgeKey, assertionContext, "connector assertion", a)
}

// VerifyConnectorAssertion checks signed against the pinned edge key, then
// that it is from issuer, for serverID and the authorization request of
// clientID with challenge and, unless it names none, redirectURI, and
// live at now within ClockSkew. It does not spend the nonce.
func VerifyConnectorAssertion(edgeKey ed25519.PublicKey, signed, issuer, serverID, clientID, challenge, redirectURI string, now time.Time) (ConnectorAssertion, error) {
	var a ConnectorAssertion
	if err := openPayload(edgeKey, assertionContext, signed, &a); err != nil {
		return ConnectorAssertion{}, err
	}
	if err := a.validate(); err != nil {
		return ConnectorAssertion{}, fmt.Errorf("%w: %v", ErrGrantMalformed, err)
	}
	switch {
	case a.Issuer != issuer || a.ServerID != serverID:
		return ConnectorAssertion{}, fmt.Errorf("%w: it names %s on %s, this is %s on %s", ErrAssertionScope, a.ServerID, a.Issuer, serverID, issuer)
	case a.ClientID != clientID || a.Challenge != challenge || (a.RedirectURI != "" && a.RedirectURI != redirectURI):
		return ConnectorAssertion{}, fmt.Errorf("%w: it was issued for another client_id, code_challenge or redirect_uri", ErrAssertionScope)
	case now.Before(a.IssuedAt.Add(-ClockSkew)):
		return ConnectorAssertion{}, fmt.Errorf("%w: issued at %s, now %s; check that the server's and the edge's clocks are set", ErrGrantNotYetValid,
			a.IssuedAt.Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	case now.After(a.ExpiresAt.Add(ClockSkew)):
		return ConnectorAssertion{}, fmt.Errorf("%w: at %s, now %s", ErrAssertionExpired, a.ExpiresAt.Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}
	return a, nil
}

// ConnectorScopes are the scopes a connector client may request. The
// member picks read, write and control on the consent page, so a client
// asks only for offline_access, a refresh token.
var ConnectorScopes = []string{"offline_access"}

// ConnectorResourceMetadata is the RFC 9728 metadata of the connector
// whose URL, resource and authorization server, is base.
func ConnectorResourceMetadata(base string) *oauthex.ProtectedResourceMetadata {
	return &oauthex.ProtectedResourceMetadata{Resource: base, AuthorizationServers: []string{base}, ScopesSupported: ConnectorScopes}
}

// ConnectorAuthServerMetadata is the RFC 8414 metadata of the connector
// authorization server whose issuer is base: client_id metadata documents
// instead of registration, PKCE with S256, and iss on every authorization
// response (RFC 9207). Servers serve it for their own connector URL and
// the edge for the universal one.
func ConnectorAuthServerMetadata(base string) any {
	// oauthex.AuthServerMeta always writes jwks_uri, and an empty URL fails
	// clients that validate it; the shallower field wins and omits it.
	return struct {
		oauthex.AuthServerMeta
		JWKSURI string `json:"jwks_uri,omitempty"`
	}{AuthServerMeta: oauthex.AuthServerMeta{
		Issuer:                                     base,
		AuthorizationEndpoint:                      base + "/authorize",
		TokenEndpoint:                              base + "/token",
		ClientIDMetadataDocumentSupported:          true,
		ScopesSupported:                            ConnectorScopes,
		ResponseTypesSupported:                     []string{"code"},
		GrantTypesSupported:                        []string{"authorization_code", "refresh_token"},
		TokenEndpointAuthMethodsSupported:          []string{"none"},
		CodeChallengeMethodsSupported:              []string{"S256"},
		AuthorizationResponseIssParameterSupported: true,
	}}
}
