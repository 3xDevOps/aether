// The platform branch the rest of the apply tests cannot reach. The
// _windows suffix is the build constraint: this file compiles only on
// Windows, where `update.apply` swaps the running executable aside.

package localgw

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestUpdateApplyInstallsOnWindows(t *testing.T) {
	pinVersion(t)
	g := updateGateway(t, &verbStubBackend{}, false)
	stubApply(t, func(context.Context, string, string) ([]string, error) {
		return []string{`C:\Users\dev\bin\aether.exe`}, nil
	})

	rec := do(g, http.MethodPost, "/local/v1/update.apply", "{}", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Updated []string `json:"updated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Updated) != 1 {
		t.Fatalf("updated = %v, want the CLI", got.Updated)
	}
}
