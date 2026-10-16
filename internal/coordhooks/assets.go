// Package coordhooks contains opt-in native hook configurations and extensions.
package coordhooks

import "embed"

// ClaudeWakeTimeout is the Claude Code hook timeout, in seconds, of the
// managed asyncRewake waiter: 7 days. Claude Code kills the waiter after it.
const ClaudeWakeTimeout = 7 * 24 * 60 * 60

// Files contains copyable hook assets, addressed by their base filenames.
//
//go:embed *.json *.ts *.js *.sh
var Files embed.FS
