package localgw

import (
	"net/http"

	"github.com/3xDevOps/Aether/internal/localops"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// localGitIdentity reports this machine's git identity so the onboarding
// wizard can prefill the one it stores on the server. An unset name or
// email comes back empty; that is a normal answer, not a failure.
func (g *Gateway) localGitIdentity(r *http.Request, _ []byte) (any, *protocol.Error) {
	name, email, err := localops.GitIdentity(r.Context())
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	return struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}{Name: name, Email: email}, nil
}
