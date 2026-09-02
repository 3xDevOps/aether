// Package desktop carries the Electron shell sources so `aether gui build`
// can package the desktop app on the user's machine without a checkout.
package desktop

import "embed"

// Source is everything electron-builder needs: the shell scripts, the
// package manifest and builder config, and the icon set. node_modules and
// dist are build outputs and never ship.
//
//go:embed main.js notify.js preload.js package.json electron-builder.yml build/icon.ico build/icons
var Source embed.FS
