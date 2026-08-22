package dashboard

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/webgate"
)

// handleCapabilities serves GET /api/v1/capabilities: what this gateway
// can do, so a client probes instead of hard-coding which transport it is
// talking to. Any member holding a token may read it - the answer is the
// same for everyone, and the methods listed still pass their own
// capability checks when called.
func (g *Gateway) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	member, _, ok := g.authenticate(w, r, false)
	if !ok {
		return
	}
	if perr := g.cfg.RPC.CheckMember(r.Context(), member); perr != nil {
		webgate.WriteError(w, webgate.StatusFor(perr.Code), perr)
		return
	}
	methods := make([]string, 0, len(apiMethods))
	for m := range apiMethods {
		methods = append(methods, m)
	}
	sort.Strings(methods)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(protocol.GatewayCapabilities{
		Gateway: "remote",
		Methods: methods,
		WS:      []string{"events", "attach"},
	})
}
