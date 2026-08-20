package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestPrintToolSnapshotsUsesStableMetadataOnly(t *testing.T) {
	original := os.Stdout
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writePipe
	printToolSnapshots(protocol.ToolSnapshotListResult{
		Snapshots: []protocol.ToolSnapshot{{
			ID:        "snap-1",
			Manifest:  protocol.ToolManifest{Executable: "omp", Version: "1.2.3", Metadata: map[string]string{"path": "/srv/private"}},
			CreatedAt: "2026-08-19T10:11:12Z",
			Active:    true,
		}},
	})
	_ = writePipe.Close()
	os.Stdout = original
	var output bytes.Buffer
	_, _ = io.Copy(&output, readPipe)
	_ = readPipe.Close()
	text := output.String()
	for _, want := range []string{"ID", "ACTIVE", "snap-1", "yes", "omp", "1.2.3", "2026-08-19T10:11:12Z"} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q: %q", want, text)
		}
	}
	if strings.Contains(text, "/srv/private") {
		t.Fatalf("output leaked metadata path: %q", text)
	}
}
