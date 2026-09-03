package selfupdate

import "testing"

func TestValidTag(t *testing.T) {
	for _, tag := range []string{"v0.0.1", "v1.2.3", "v0.1.2-alpha.12", "v10.20.30-rc-1"} {
		if !ValidTag(tag) {
			t.Errorf("ValidTag(%q) = false, want true", tag)
		}
	}
	// A tag is interpolated into the pinned releases URL, so anything that
	// could escape it - or that names a local build rather than a release -
	// is refused rather than sanitized.
	for _, tag := range []string{
		"", "1.2.3", "v1.2", "v1.2.3.4", "v01.2.3", "v1.2.3+build",
		"v1.2.3-4-gabc1234", "v1.2.3-dirty", "v1.2.3/../../etc",
		"https://example.com/v1.2.3", "v1.2.3 v1.2.4", "latest",
	} {
		if ValidTag(tag) {
			t.Errorf("ValidTag(%q) = true, want false", tag)
		}
	}
}
