// Package web carries the built dashboard SPA as an embedded filesystem.
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
