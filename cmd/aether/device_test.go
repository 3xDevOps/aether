package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestConfirmDeviceApprovalShowsWhomItAdmits(t *testing.T) {
	existing := protocol.MemberDeviceLookupResult{
		Device:   protocol.Device{ID: "dev-1", Label: "mallory-laptop", Provider: "github", Account: "octo", Fingerprint: "SHA256:abc"},
		MemberID: "m-octo", DisplayName: "Octo Cat", Role: "admin",
	}
	invited := protocol.MemberDeviceLookupResult{
		Device: protocol.Device{ID: "dev-2", Label: "dana-laptop", Provider: "github", Account: "dana", InvitationID: "inv-1"},
		Role:   "collaborator",
	}
	for _, tc := range []struct {
		found  protocol.MemberDeviceLookupResult
		answer string
		want   bool
		shows  []string
	}{
		{existing, "\n", false, []string{`device "mallory-laptop", key SHA256:abc`, "signed in as: github account octo", "admits it as: Octo Cat (m-octo), admin", "[y/N]"}},
		{existing, "", false, nil},
		{existing, "Yes\n", true, nil},
		{invited, "y\n", true, []string{"admits it as: a new member, collaborator", "accepts invitation inv-1"}},
	} {
		var out bytes.Buffer
		ok, err := confirmDeviceApproval(strings.NewReader(tc.answer), &out, tc.found)
		if err != nil || ok != tc.want {
			t.Fatalf("answer %q = %v, %v; want %v", tc.answer, ok, err, tc.want)
		}
		for _, want := range tc.shows {
			if !strings.Contains(out.String(), want) {
				t.Errorf("prompt lacks %q:\n%s", want, out.String())
			}
		}
	}
}
