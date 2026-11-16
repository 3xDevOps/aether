package localops

import "io/fs"

// ownedByUser is true on windows, where the desktop app lives only under
// the user's own LOCALAPPDATA.
func ownedByUser(fs.FileInfo) bool { return true }
