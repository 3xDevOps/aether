package assistant

import _ "embed"

// Instructions is the text written as AGENTS.md and CLAUDE.md in the
// assistant's working directory.
//
//go:embed instructions.md
var Instructions string
