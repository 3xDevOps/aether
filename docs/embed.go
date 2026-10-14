// Package docs carries the public guides as an embedded filesystem.
package docs

import "embed"

//go:embed *.md
var FS embed.FS
