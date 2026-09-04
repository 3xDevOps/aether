package selfupdate

import "os"

// fileOwner has no uid to report on Windows, where the authorized path
// never runs: self-update is refused there before it is reached.
func fileOwner(os.FileInfo) (uint32, bool) { return 0, false }
