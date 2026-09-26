// Package coordhooks contains opt-in native hook configurations and extensions.
package coordhooks

import "embed"

// Files contains copyable hook assets, addressed by their base filenames.
//
//go:embed *.json *.ts *.js *.sh
var Files embed.FS
