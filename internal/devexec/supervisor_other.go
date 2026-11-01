//go:build !linux

package devexec

import "errors"

// Run requires Linux process groups, subreaping and /proc child identities.
func Run(_ string, _ string, _ bool, _ []string) (int, error) {
	return 125, errors.New("owned development executions require Linux")
}

func RunAttached(int, []string) (int, error) {
	return 125, errors.New("attached executions require Linux")
}
