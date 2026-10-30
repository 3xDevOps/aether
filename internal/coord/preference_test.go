package coord

import (
	"context"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// A run, on whoever's account, has no path to any member's preferences:
// the coordination socket serves none of their methods.
func TestRunsCannotReachPreferences(t *testing.T) {
	h := newHarness(t, 1)
	for _, method := range []string{protocol.MethodPreferenceList, protocol.MethodPreferenceAdd, protocol.MethodPreferenceUpdate,
		protocol.MethodPreferenceDelete, protocol.MethodAssistantPreferences, protocol.MethodAssistantRemember, protocol.MethodAssistantForget} {
		resp := h.svc.handle(context.Background(), h.run(0), []byte(`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":{}}`))
		if resp.Error == nil || resp.Error.Code != protocol.CodeMethodNotFound {
			t.Errorf("%s from a session = %+v, want method not found", method, resp.Error)
		}
	}
}
