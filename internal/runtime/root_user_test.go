package runtime

import "testing"

func TestRootUserMatchesUidByValue(t *testing.T) {
	for _, u := range []string{"0", "root", "0:0", "00", "00:00", "0:1000", "root:root"} {
		if !RootUser(u) {
			t.Errorf("RootUser(%q) = false, want true (resolves to uid 0)", u)
		}
	}
	for _, u := range []string{"1000", "1000:1000", "nobody", "", "0x0", "10"} {
		if RootUser(u) {
			t.Errorf("RootUser(%q) = true, want false", u)
		}
	}
}
