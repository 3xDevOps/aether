package vm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// consoleLogBytes bounds console.log. The output before it is kept in
// console.log.1, so a VM holds at most twice this of its serial console,
// whatever its guest writes.
const consoleLogBytes = 1 << 20

// consoleLogFile writes a VM's serial console to its file, which it starts
// again once the file holds consoleLogBytes, keeping the full one as
// path.1.
type consoleLogFile struct {
	path string
	f    *os.File
	size int64
}

func openConsoleLog(path string) (*consoleLogFile, error) {
	if err := os.Remove(path + ".1"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("vm monitor: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("vm monitor: %w", err)
	}
	return &consoleLogFile{path: path, f: f}, nil
}

func (c *consoleLogFile) Write(p []byte) (int, error) {
	if c.size > 0 && c.size+int64(len(p)) > consoleLogBytes {
		if err := c.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := c.f.Write(p)
	c.size += int64(n)
	return n, err
}

func (c *consoleLogFile) rotate() error {
	err := errors.Join(c.f.Close(), os.Rename(c.path, c.path+".1"))
	if err != nil {
		return fmt.Errorf("vm monitor: rotate %s: %w", c.path, err)
	}
	if c.f, err = os.OpenFile(c.path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600); err != nil {
		return fmt.Errorf("vm monitor: %w", err)
	}
	c.size = 0
	return nil
}
