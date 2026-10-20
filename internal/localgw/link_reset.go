package localgw

import (
	"fmt"
	"net/http"
	"os"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// localLinkReset forgets every server this computer linked to: the gateway
// drops its connection, forwards and syncs, then the Aether config
// directory (links, edge device key and tokens, workspace selection) is
// deleted. The SSH key under ~/.ssh and the linked clone are the user's
// own and stay.
func (g *Gateway) localLinkReset(*http.Request, []byte) (any, *protocol.Error) {
	dir, err := cli.Dir()
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	g.local.switching.Lock()
	defer g.local.switching.Unlock()
	g.moveTo(cli.Config{}, nil)
	if err := os.RemoveAll(dir); err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: fmt.Sprintf("delete %s: %v", dir, err)}
	}
	return struct {
		Deleted string `json:"deleted"`
	}{Deleted: dir}, nil
}
