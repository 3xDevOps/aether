//go:build !linux

package devexec

import "errors"

// Run requires Linux process groups, subreaping and /proc child identities.
func Run(_ string, _ []string) (int, error) {
	return 125, errors.New("owned development executions require Linux")
}
