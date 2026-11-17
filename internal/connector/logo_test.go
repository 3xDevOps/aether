package connector

import (
	"os"
	"testing"
)

func TestConsentLogoIsTheDashboardsFile(t *testing.T) {
	want, err := os.ReadFile("../../web/public/cloudfield-logo-small.svg")
	if err != nil {
		t.Fatal(err)
	}
	if consentLogo != string(want) {
		t.Error("internal/connector/cloudfield-logo-small.svg differs from web/public/cloudfield-logo-small.svg; copy it again")
	}
}
