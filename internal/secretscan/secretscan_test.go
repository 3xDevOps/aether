package secretscan

import "testing"

const genericSecretSample = "QmFzZTY0c2VjcmV0LWFldGhlci10ZXN0LTQy"

func TestScanDetectsGenericSecret(t *testing.T) {
	hits := Scan("settings.json", []byte("token="+genericSecretSample))
	if len(hits) == 0 {
		t.Fatal("expected gitleaks finding")
	}
	if hits[0].Path != "settings.json" || hits[0].Location == "" || hits[0].Kind == "" {
		t.Fatalf("finding = %+v", hits[0])
	}
}

func TestScanPassesPlainContent(t *testing.T) {
	if hits := Scan("Dockerfile", []byte("FROM ubuntu:24.04\nRUN apt-get update\n")); len(hits) != 0 {
		t.Fatalf("unexpected findings: %+v", hits)
	}
}
