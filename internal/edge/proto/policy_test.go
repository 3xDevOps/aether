package edgeproto

import "testing"

func TestParseAccessPolicy(t *testing.T) {
	for in, want := range map[string]AccessPolicy{
		"":                 PolicyApprovedDevices,
		"approved-devices": PolicyApprovedDevices,
		"account":          PolicyAccount,
	} {
		if got, err := ParseAccessPolicy(in); err != nil || got != want {
			t.Errorf("ParseAccessPolicy(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"Account", "approved_devices", "approved", "none", "open", " account", "account\n"} {
		if got, err := ParseAccessPolicy(in); err == nil {
			t.Errorf("ParseAccessPolicy(%q) = %q, want an error", in, got)
		}
	}
}
