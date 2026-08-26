package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// Push uploads discovered files via profile.push using a content-addressed
// delta against the server's current head. The client never calls PinRun.
func Push(c *protocol.Client, harness string, files []LocalFile, allowSecret []string, workspaceID string) (protocol.ProfileSnapshot, error) {
	params, err := BuildPushParams(c, harness, files, allowSecret, workspaceID)
	if err != nil {
		return protocol.ProfileSnapshot{}, err
	}
	var res protocol.ProfilePushResult
	if err := c.Call(protocol.MethodProfilePush, params, &res); err != nil {
		return protocol.ProfileSnapshot{}, err
	}
	return res.Snapshot, nil
}

// BuildPushParams constructs a content-addressed delta push. Status is
// consulted so only blobs the server does not already have are sent.
func BuildPushParams(c *protocol.Client, harness string, files []LocalFile, allowSecret []string, workspaceID string) (protocol.ProfilePushParams, error) {
	known := map[string]struct{}{}
	if c != nil {
		var st protocol.ProfileStatusResult
		if err := c.Call(protocol.MethodProfileStatus, protocol.ProfileStatusParams{Harness: harness}, &st); err == nil {
			for _, f := range st.Files {
				if f.Digest != "" {
					known[f.Digest] = struct{}{}
				}
			}
		}
	}
	params := protocol.ProfilePushParams{
		Harness:     harness,
		WorkspaceID: workspaceID,
		AllowSecret: allowSecret,
		Paths:       make([]protocol.ProfileFile, 0, len(files)),
	}
	for _, f := range files {
		sum := sha256.Sum256(f.Content)
		digest := hex.EncodeToString(sum[:])
		params.Paths = append(params.Paths, protocol.ProfileFile{
			Path:   f.Path,
			Mode:   f.Mode,
			Digest: digest,
		})
		if _, ok := known[digest]; ok {
			continue
		}
		params.Blobs = append(params.Blobs, protocol.ProfileBlob{
			Digest:  digest,
			Content: f.Content,
		})
		known[digest] = struct{}{}
	}
	return params, nil
}

// Status fetches the latest snapshot for harness.
func Status(c *protocol.Client, harness string) (protocol.ProfileStatusResult, error) {
	var res protocol.ProfileStatusResult
	err := c.Call(protocol.MethodProfileStatus, protocol.ProfileStatusParams{Harness: harness}, &res)
	return res, err
}

// Rollback points the member+harness head at snapshotID without mutating a pin.
func Rollback(c *protocol.Client, harness, snapshotID string) (protocol.ProfileSnapshot, error) {
	var res protocol.ProfileRollbackResult
	err := c.Call(protocol.MethodProfileRollback, protocol.ProfileRollbackParams{
		Harness:    harness,
		SnapshotID: snapshotID,
	}, &res)
	if err != nil {
		return protocol.ProfileSnapshot{}, err
	}
	return res.Snapshot, nil
}

// StatusNotice is printed with profile status so operators know run-local
// edits never sync back.
const StatusNotice = "run-local profile edits are writable for the life of the run, discarded at teardown, and never sync back or mutate the pin"

func FormatStatus(res protocol.ProfileStatusResult) string {
	if res.Snapshot == nil {
		return "no profile snapshot\n" + StatusNotice + "\n"
	}
	return fmt.Sprintf("snapshot %s\ndigest %s\ncreated_at %s\n%s\n",
		res.Snapshot.ID, res.Snapshot.Digest, res.Snapshot.CreatedAt, StatusNotice)
}
