package devexec

import (
	"io"

	"github.com/pkg/sftp"
)

// ServeSFTP serves the SFTP protocol on rwc with this process's own file
// authority. Relative paths resolve against its working directory.
func ServeSFTP(rwc io.ReadWriteCloser) error {
	server, err := sftp.NewServer(rwc)
	if err != nil {
		return err
	}
	return server.Serve()
}
