//go:build !unix

package serversetup

import "os"

func fileOwner(os.FileInfo) (uid, gid int) { return -1, -1 }
