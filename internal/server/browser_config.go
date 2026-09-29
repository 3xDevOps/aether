package server

import "github.com/3xDevOps/Aether/internal/version"

const browserImageRepo = "ghcr.io/3xdevops/aether-browser"

// DefaultBrowserImage follows the binary's release, never a mutable latest
// alias. Untagged source builds use the image built by make browser-image;
// operators can select a published version or digest with --browser-image.
var DefaultBrowserImage = defaultBrowserImage(version.Version)

func defaultBrowserImage(buildVersion string) string {
	tag := releaseImageTag(buildVersion)
	if tag == "latest" || tag != buildVersion {
		return "aether/browser:test"
	}
	return browserImageRepo + ":" + buildVersion
}

// ci filter check; not for merge
