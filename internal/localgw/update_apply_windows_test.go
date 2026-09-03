// The platform branch the rest of the apply tests cannot reach. The
// _windows suffix is the build constraint: this file compiles only on
// Windows, where `update.apply` refuses rather than swapping a running
// executable out from under itself.

package localgw

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestUpdateApplyRefusesOnWindows(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, true)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		t.Fatal("Windows cannot rename over a running executable; nothing may be downloaded")
		return nil, nil
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	perr := decodeError(t, rec.Body.Bytes())
	if perr.Code != protocol.CodeInvalidState {
		t.Fatalf("code = %d, want %d", perr.Code, protocol.CodeInvalidState)
	}
	if !strings.Contains(perr.Message, "not supported on Windows") {
		t.Fatalf("message = %q, want the documented Windows refusal", perr.Message)
	}
	// The refusal has to point somewhere: docs/install.md says a Windows
	// client upgrades by downloading the release binary by hand.
	if !strings.Contains(perr.Message, "/releases") {
		t.Fatalf("message = %q, want it to name the releases page", perr.Message)
	}
	select {
	case <-g.Exit():
		t.Fatal("a refused update asked the process to exit")
	default:
	}
}

// The check verb has no platform branch, but it does report one: a Windows
// client is told an update exists and that it cannot install it itself.
func TestUpdateCheckReportsNoSelfUpdateOnWindows(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, false)

	rec := do(g, http.MethodPost, "/local/v1/update.check", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		CLI struct {
			UpdateAvailable bool `json:"update_available"`
			CanSelfUpdate   bool `json:"can_self_update"`
		} `json:"cli"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.CLI.UpdateAvailable {
		t.Error("update_available = false; Windows still gets told about a release")
	}
	if got.CLI.CanSelfUpdate {
		t.Error("can_self_update = true on Windows")
	}
}
