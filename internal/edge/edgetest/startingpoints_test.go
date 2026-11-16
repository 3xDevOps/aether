package edgetest

import (
	"fmt"
	"slices"
	"testing"

	"github.com/3xDevOps/Aether/internal/selfupdate"
)

// start is a release hosts upgrade from, with the build its image records.
type start struct {
	tag   string
	build int
}

// chooseStarts keeps each release a pointer names once, unless it is
// above ours by (build, hotfix): hosts on that channel never take ours,
// as with a hotfix for an older line. A zero build is not a channel
// release, which qualifies from every pointer.
func chooseStarts(pointers []selfupdate.Pointer, ours selfupdate.Mark) []start {
	var starts []start
	for _, p := range pointers {
		above := p.Build > ours.Build || p.Build == ours.Build && p.Hotfix > ours.Hotfix
		if ours.Build > 0 && above || slices.ContainsFunc(starts, func(s start) bool { return s.tag == p.Tag }) {
			continue
		}
		starts = append(starts, start{p.Tag, p.Build})
	}
	return starts
}

func TestChooseStarts(t *testing.T) {
	ptr := func(tag string, build, hotfix int) selfupdate.Pointer {
		return selfupdate.Pointer{Tag: tag, Build: build, Hotfix: hotfix}
	}
	stable, beta, nightly := ptr("v0.10.0", 500, 0), ptr("v0.11.0-beta.2", 540, 0), ptr("v0.11.0-nightly.9", 560, 0)
	for _, tc := range []struct {
		name     string
		pointers []selfupdate.Pointer
		ours     selfupdate.Mark
		want     string
	}{
		{"a nightly upgrades from all three", []selfupdate.Pointer{stable, beta, nightly}, selfupdate.Mark{Build: 561}, "[{v0.10.0 500} {v0.11.0-beta.2 540} {v0.11.0-nightly.9 560}]"},
		{"a hotfix for an older line skips the newer stable and beta", []selfupdate.Pointer{stable, beta}, selfupdate.Mark{Build: 400, Hotfix: 1}, "[]"},
		{"a hotfix of the current stable upgrades from it", []selfupdate.Pointer{stable, beta}, selfupdate.Mark{Build: 500, Hotfix: 1}, "[{v0.10.0 500}]"},
		{"a promotion upgrades from the same build", []selfupdate.Pointer{stable, ptr("v0.11.0-beta.2", 540, 0)}, selfupdate.Mark{Build: 540}, "[{v0.10.0 500} {v0.11.0-beta.2 540}]"},
		{"a build below a hotfix of the same build skips it", []selfupdate.Pointer{ptr("v0.10.1", 500, 1)}, selfupdate.Mark{Build: 500}, "[]"},
		{"stable and beta naming one release start once", []selfupdate.Pointer{stable, ptr("v0.10.0", 500, 0)}, selfupdate.Mark{Build: 541}, "[{v0.10.0 500}]"},
		{"no build given qualifies from every pointer", []selfupdate.Pointer{stable, nightly}, selfupdate.Mark{}, "[{v0.10.0 500} {v0.11.0-nightly.9 560}]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fmt.Sprint(chooseStarts(tc.pointers, tc.ours)); got != tc.want {
				t.Fatalf("chooseStarts = %s, want %s", got, tc.want)
			}
		})
	}
}
