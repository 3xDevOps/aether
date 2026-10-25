package edgeproto

import (
	"crypto/ed25519"
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
// to PathMCPChoose on the sign-in origin, which sends the browser back
// with the server the signed-in account chose and a ConnectorAssertion.
// The edge serves the two metadata documents itself.
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

// Query parameters the edge adds to a PathUniversalMCPAuthorize request
// it sends to a server.
const (
	ParamServer    = "server"
	ParamAssertion = "edge_assertion"
)

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
// request of ClientID with PKCE challenge Challenge on the universal
// connector. Nonce makes it single-use: the server spends it. Issuer is
// the edge's canonical relay origin.
type ConnectorAssertion struct {
	Issuer    string    `json:"issuer"`
	ServerID  string    `json:"server_id"`
	Nonce     string    `json:"nonce"`
	Account   Account   `json:"account"`
	ClientID  string    `json:"client_id"`
	Challenge string    `json:"challenge"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
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
	case !validRequiredText(a.ClientID, maxShortText) || !validRequiredText(a.Challenge, maxShortText):
		return errors.New("client id or challenge is empty, too long or has control characters")
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
// clientID with challenge, and live at now within ClockSkew. It does not
// spend the nonce.
func VerifyConnectorAssertion(edgeKey ed25519.PublicKey, signed, issuer, serverID, clientID, challenge string, now time.Time) (ConnectorAssertion, error) {
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
	case a.ClientID != clientID || a.Challenge != challenge:
		return ConnectorAssertion{}, fmt.Errorf("%w: it was issued for another client_id or code_challenge", ErrAssertionScope)
	case now.Before(a.IssuedAt.Add(-ClockSkew)):
		return ConnectorAssertion{}, fmt.Errorf("%w: issued at %s, now %s; check that the server's and the edge's clocks are set", ErrGrantNotYetValid,
			a.IssuedAt.Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	case now.After(a.ExpiresAt.Add(ClockSkew)):
		return ConnectorAssertion{}, fmt.Errorf("%w: at %s, now %s", ErrAssertionExpired, a.ExpiresAt.Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}
	return a, nil
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
		ScopesSupported:                            []string{"offline_access"},
		ResponseTypesSupported:                     []string{"code"},
		GrantTypesSupported:                        []string{"authorization_code", "refresh_token"},
		TokenEndpointAuthMethodsSupported:          []string{"none"},
		CodeChallengeMethodsSupported:              []string{"S256"},
		AuthorizationResponseIssParameterSupported: true,
	}}
}
