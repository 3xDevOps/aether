package edgeproto

import "testing"

// A banner is structured only by its first line, and only when no field
// could drive the terminal its prose is printed to.
func TestParseRefusedBanner(t *testing.T) {
	r := &Refused{Kind: RefusedDeviceRevoked, Device: "laptop"}
	for _, ok := range []*Refused{
		{Kind: RefusedDevicePending, ApprovalCode: "ABCD-EFGH"},
		{Kind: RefusedDevicePending, DeviceID: "dev-7k2m9q4xbd"},
		{Kind: RefusedDevicePending, DeviceID: "01hzy3v8k2m9q4xbd7n5p6r0st"},
	} {
		if _, structured := ParseRefusedBanner(ok.Banner()); !structured {
			t.Errorf("ParseRefusedBanner(%q) is unstructured", ok.Banner())
		}
	}
	if got, ok := ParseRefusedBanner(r.Banner()); !ok || *got != *r {
		t.Fatalf("ParseRefusedBanner(%q) = %+v, %v", r.Banner(), got, ok)
	}
	for _, banner := range []string{
		r.Error() + "\n",
		"note\n" + r.Banner(),
		(&Refused{Kind: RefusedDeviceRevoked, Device: "laptop\x1b[2J"}).Banner(),
		refusedBanner + "{}\n",
		(&Refused{Kind: RefusedDevicePending, ApprovalCode: "ABCD-EFGH; sudo touch /tmp/x;#"}).Banner(),
		(&Refused{Kind: RefusedDevicePending, ApprovalCode: "abcd-efgh"}).Banner(),
		(&Refused{Kind: RefusedDevicePending, DeviceID: "dev-7k2m9q4xbd; sudo touch /tmp/x;#"}).Banner(),
		(&Refused{Kind: RefusedDevicePending, DeviceID: "$(touch /tmp/x)"}).Banner(),
	} {
		if got, ok := ParseRefusedBanner(banner); ok {
			t.Errorf("ParseRefusedBanner(%q) = %+v, want unstructured", banner, got)
		}
	}
}
