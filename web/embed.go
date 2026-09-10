// Package web carries the built static dashboard as an embedded filesystem.
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
