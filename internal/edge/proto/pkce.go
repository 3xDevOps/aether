package edgeproto

// NewVerifier returns a PKCE code verifier for one sign-in at a provider.
// It is a token, 43 characters, the minimum length RFC 7636 allows.
func NewVerifier() string { return NewToken() }
