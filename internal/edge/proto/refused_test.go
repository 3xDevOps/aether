package edgeproto

import "testing"

// A banner is structured only by its first line, and only when no field
// could drive the terminal its prose is printed to.
func TestParseRefusedBanner(t *testing.T) {
	r := &Refused{Kind: RefusedDeviceRevoked, Device: "laptop"}
	if got, ok := ParseRefusedBanner(r.Banner()); !ok || *got != *r {
		t.Fatalf("ParseRefusedBanner(%q) = %+v, %v", r.Banner(), got, ok)
	}
	for _, banner := range []string{
		r.Error() + "\n",
		"note\n" + r.Banner(),
		(&Refused{Kind: RefusedDeviceRevoked, Device: "laptop\x1b[2J"}).Banner(),
		refusedBanner + "{}\n",
	} {
		if got, ok := ParseRefusedBanner(banner); ok {
			t.Errorf("ParseRefusedBanner(%q) = %+v, want unstructured", banner, got)
		}
	}
}
