package protocol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestWorkspaceShellRequestJSONRoundTrip(t *testing.T) {
	request := WorkspaceShellRequest{
		Workspace:              WorkspaceSelector{ID: "ws_1"},
		Mode:                   WorkspaceShellModeBootstrapTools,
		Harness:                "",
		VerificationExecutable: "omp",
		Resume:                 true,
		Reset:                  false,
		Cols:                   120,
		Rows:                   40,
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var got WorkspaceShellRequest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, request) {
		t.Fatalf("round trip changed request: got %+v, want %+v", got, request)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("round-tripped request invalid: %v", err)
	}
}

func TestWorkspaceShellRequestRejectsInvalidWorkspaceSelector(t *testing.T) {
	for name, request := range map[string]WorkspaceShellRequest{
		"missing": {Mode: WorkspaceShellModeBootstrapTools},
		"both": {
			Workspace: WorkspaceSelector{ID: "ws_1", Name: "project"},
			Mode:      WorkspaceShellModeBootstrapTools,
		},
		"invalid mode": {
			Workspace: WorkspaceSelector{ID: "ws_1"},
			Mode:      WorkspaceShellMode("invalid"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := request.Validate(); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
}

func TestToolSnapshotDTOHasStableMetadataAndNoHostPath(t *testing.T) {
	snapshot := ToolSnapshot{
		ID:          "snapshot_1",
		WorkspaceID: "ws_1",
		MemberID:    "member_1",
		Digest:      "sha256:abc",
		Manifest: ToolManifest{
			Executable: "omp",
			Version:    "1.2.3",
			Metadata:   map[string]string{"source": "bootstrap"},
		},
		CreatedAt: "2026-08-19T10:11:12Z",
		Active:    true,
	}
	raw, err := json.Marshal(ToolSnapshotListResult{Active: &snapshot, Snapshots: []ToolSnapshot{snapshot}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "host_path") || strings.Contains(string(raw), "absolute_path") {
		t.Fatalf("tool DTO exposes a host path field: %s", raw)
	}
	var got ToolSnapshotListResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Active == nil || got.Active.Digest != snapshot.Digest || got.Active.CreatedAt != snapshot.CreatedAt {
		t.Fatalf("snapshot metadata changed during round trip: %+v", got)
	}
}

func TestToolSnapshotControlParamsAllowEmptyOptionalFields(t *testing.T) {
	params := ToolSnapshotVerifyParams{Workspace: WorkspaceSelector{ID: "ws_1"}}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "verification_executable") {
		t.Fatalf("empty verification executable should be omitted: %s", raw)
	}
	var got ToolSnapshotVerifyParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Workspace != params.Workspace || got.VerificationExecutable != "" {
		t.Fatalf("empty optional field changed: %+v", got)
	}
}

func TestToolSnapshotCreatedAtUsesRFC3339(t *testing.T) {
	when := time.Date(2026, 8, 19, 10, 11, 12, 0, time.UTC)
	dto := ToolSnapshotFromDomain(testToolSnapshot(when))
	if dto.CreatedAt != "2026-08-19T10:11:12Z" {
		t.Fatalf("created_at = %q", dto.CreatedAt)
	}
}

func testToolSnapshot(when time.Time) domain.ToolSnapshot {
	return domain.ToolSnapshot{
		ID:          "snapshot_1",
		WorkspaceID: "ws_1",
		MemberID:    "member_1",
		Digest:      "sha256:abc",
		Manifest: domain.ToolManifest{
			Executable: "omp",
			Version:    "1.2.3",
			Metadata:   map[string]string{"source": "bootstrap"},
		},
		CreatedAt: when,
	}
}
