package localgw

import (
	"fmt"
	"net/http"
	"os"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// localLinkReset leaves the SSH key under ~/.ssh and the linked clone alone:
// they are the user's own.
func (g *Gateway) localLinkReset(*http.Request, []byte) (any, *protocol.Error) {
	dir, err := cli.Dir()
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	g.local.switching.Lock()
	defer g.local.switching.Unlock()
	g.local.edge.reset()
	g.moveTo(cli.Config{}, nil)
	if err := os.RemoveAll(dir); err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: fmt.Sprintf("delete %s: %v", dir, err)}
	}
	return struct {
		Deleted string `json:"deleted"`
	}{Deleted: dir}, nil
}
