package profile

import "testing"

const genericSecretSample = "QmFzZTY0c2VjcmV0LWFldGhlci10ZXN0LTQy"

func TestScanFilesHonorsAllowList(t *testing.T) {
	files := []File{{Path: "settings.json", Mode: 0o644, Content: []byte("token=" + genericSecretSample)}}
	if err := ScanFiles(files, nil); err == nil {
		t.Fatal("expected denial")
	}
	if err := ScanFiles(files, map[string]bool{"settings.json": true}); err != nil {
		t.Fatalf("allow: %v", err)
	}
}
