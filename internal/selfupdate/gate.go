package selfupdate

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Ahead reports whether a is newer than b in the order the server's
// client gate uses: by (build, hotfix) when both carry a build, else by
// version alone, as before channels. A client that predates builds, or a
// server that does, keeps the version rule on both ends.
func Ahead(a, b Release) bool {
	if a.Build == 0 || b.Build == 0 {
		return Behind(b.Version, a.Version)
	}
	return Newer(a, b)
}

// BannerVersion is r's version as an Aether client's SSH banner carries
// it: the version, then its build as semver build metadata, "+b.412", or
// "+b.412.h.1" for a hotfix. Build 0 adds nothing. Servers from before
// builds drop build metadata when they order versions.
func BannerVersion(r Release) string {
	switch {
	case r.Build == 0:
		return r.Version
	case r.Hotfix == 0:
		return fmt.Sprintf("%s+b.%d", r.Version, r.Build)
	}
	return fmt.Sprintf("%s+b.%d.h.%d", r.Version, r.Build, r.Hotfix)
}

var bannerBuild = regexp.MustCompile(`^b\.([1-9][0-9]{0,8})(?:\.h\.([1-9][0-9]{0,8}))?$`)

// ParseBannerVersion reads what BannerVersion wrote. Build metadata in any
// other form is dropped and the build reads as 0, which orders the client
// by its version, as before builds.
func ParseBannerVersion(v string) Release {
	core, meta, found := strings.Cut(v, "+")
	r := Release{Version: core}
	m := bannerBuild.FindStringSubmatch(meta)
	if !found || m == nil {
		return r
	}
	r.Build, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		r.Hotfix, _ = strconv.Atoi(m[2])
	}
	return r
}

// PointerMissing reports whether err is FetchPointer's 404 for the
// pointer itself: the channel has no pointer published. A 404 for its
// signature is not that; the pointer is there and unverifiable.
func PointerMissing(err error) bool {
	var status *statusError
	return errors.As(err, &status) && status.code == http.StatusNotFound && !strings.HasSuffix(status.url, ".sig")
}

func (c Check) LatestRelease() Release {
	return Release{Version: c.Latest, Build: c.LatestBuild, Hotfix: c.LatestHotfix}
}
