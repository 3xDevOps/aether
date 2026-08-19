package dashboard

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// TestDiskEndpointReportsTheDataDirectory: the status bar's gauge needs a
// number the frozen server.info result cannot carry, so the gateway serves
// it here - behind a token like every other read, and answering
// unavailable rather than zero when it has no directory to measure.
func TestDiskEndpointReportsTheDataDirectory(t *testing.T) {
	e := newEnv(t)
	e.gw.disk = disk.NewCache(t.TempDir(), 0)
	token := e.mint(e.viewer)

	status, body := e.get(token, "/api/v1/disk")
	if status != http.StatusOK {
		t.Fatalf("disk status = %d, want 200 (%s)", status, body)
	}
	var got diskResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode disk: %v", err)
	}
	if got.TotalBytes == 0 || got.UsedBytes > got.TotalBytes {
		t.Fatalf("disk usage = %d of %d, want a used share of a non-zero total",
			got.UsedBytes, got.TotalBytes)
	}
	if got.FreeBytes == 0 || got.FreeBytes > got.TotalBytes {
		t.Fatalf("disk free = %d of %d, want the headroom the scheduler's floor reads",
			got.FreeBytes, got.TotalBytes)
	}

	if status, body = e.get("", "/api/v1/disk"); status != http.StatusUnauthorized {
		t.Fatalf("untokened disk status = %d, want 401 (%s)", status, body)
	}

	// A statfs failure must not echo the server's data-directory path.
	gone := filepath.Join(t.TempDir(), "unmounted")
	e.gw.disk = disk.NewCache(gone, 0)
	status, body = e.get(token, "/api/v1/disk")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("unreadable disk status = %d, want 503 (%s)", status, body)
	}
	if strings.Contains(string(body), gone) {
		t.Fatalf("disk error echoed the server path: %s", body)
	}

	e.gw.disk = nil
	status, body = e.get(token, "/api/v1/disk")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured disk status = %d, want 503 (%s)", status, body)
	}
	if code := e.errorCode(body); code != protocol.CodeUnavailable {
		t.Fatalf("unconfigured disk code = %d, want %d", code, protocol.CodeUnavailable)
	}

	// A token outlives its member only as a key, not as authority: the
	// disk gauge re-validates membership like every other endpoint.
	e.gw.disk = disk.NewCache(t.TempDir(), 0)
	if err := e.db.DeleteMember(t.Context(), e.viewer); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	status, body = e.get(token, "/api/v1/disk")
	if status != http.StatusForbidden {
		t.Fatalf("removed member's disk status = %d, want 403 (%s)", status, body)
	}
}
