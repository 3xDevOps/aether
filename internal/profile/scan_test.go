package profile

import "testing"

const genericSecretSample = "QmFzZTY0c2VjcmV0LWFldGhlci10ZXN0LTQy"

func TestScanContentDetectsGenericSecret(t *testing.T) {
	hits := ScanContent("settings.json", []byte("token="+genericSecretSample))
	if len(hits) == 0 {
		t.Fatal("expected gitleaks finding")
	}
	if hits[0].Path != "settings.json" || hits[0].Location == "" || hits[0].Kind == "" {
		t.Fatalf("finding = %+v", hits[0])
	}
}

func TestScanFilesHonorsAllowList(t *testing.T) {
	files := []File{{Path: "settings.json", Mode: 0o644, Content: []byte("token=" + genericSecretSample)}}
	if err := ScanFiles(files, nil); err == nil {
		t.Fatal("expected denial")
	}
	if err := ScanFiles(files, map[string]bool{"settings.json": true}); err != nil {
		t.Fatalf("allow: %v", err)
	}
}
