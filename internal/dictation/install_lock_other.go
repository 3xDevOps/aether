//go:build !linux

package dictation

import "errors"

func lockInstall(string) (func(), error) {
	return nil, errors.New("dictation runs only on Linux")
}
