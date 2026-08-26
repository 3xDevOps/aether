package protocol

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestRunWireIncludesProfileSnapshotID(t *testing.T) {
	r := &domain.Run{
		ID: "run_1", WorkspaceID: "ws_1", MemberID: "m_1", Task: "t",
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning,
		CreatedAt: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(RunFromDomain(r))
	if err != nil {
		t.Fatal(err)
	}
	if json.Valid(raw) && containsKey(raw, "profile_snapshot_id") {
		t.Fatalf("empty pin should omit profile_snapshot_id: %s", raw)
	}
	r.ProfileSnapshotID = "snap_1"
	raw, err = json.Marshal(RunFromDomain(r))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["profile_snapshot_id"] != "snap_1" {
		t.Fatalf("profile_snapshot_id = %v", m["profile_snapshot_id"])
	}
}
func TestReadLineAcceptsMaximumProfilePush(t *testing.T) {
	params := ProfilePushParams{Harness: "claude"}
	for i := range 20 {
		content := bytes.Repeat([]byte{byte(i)}, 1<<20)
		digest := fmt.Sprintf("%x", sha256.Sum256(content))
		params.Paths = append(params.Paths, ProfileFile{
			Path:   fmt.Sprintf("file-%02d.bin", i),
			Digest: digest,
		})
		params.Blobs = append(params.Blobs, ProfileBlob{Digest: digest, Content: content})
	}
	rawParams, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	rawRequest, err := json.Marshal(Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  MethodProfilePush,
		Params:  rawParams,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLine(bufio.NewReader(bytes.NewReader(append(rawRequest, '\n')))); err != nil {
		t.Fatalf("maximum valid profile push rejected at %d bytes: %v", len(rawRequest), err)
	}
}

func containsKey(raw []byte, key string) bool {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}

func TestProfileMethodNames(t *testing.T) {
	if MethodProfilePush != "profile.push" || MethodProfileStatus != "profile.status" || MethodProfileRollback != "profile.rollback" {
		t.Fatalf("methods = %s %s %s", MethodProfilePush, MethodProfileStatus, MethodProfileRollback)
	}
}
