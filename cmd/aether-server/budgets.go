package main

import (
	"errors"
	"strconv"

	"github.com/3xDevOps/Aether/internal/scheduler"
)

// Validate in flag.Value.Set so serve, install, config set and edited config
// files share the same rejection behavior before any server state is created.
type runCPUValue float64

func (v *runCPUValue) String() string { return strconv.FormatFloat(float64(*v), 'g', -1, 64) }
func (v *runCPUValue) Set(text string) error {
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return err
	}
	if err := scheduler.ValidateRunBudgets(value, 0, 0); err != nil {
		return err
	}
	*v = runCPUValue(value)
	return nil
}

type runCountValue int64

func (v *runCountValue) String() string { return strconv.FormatInt(int64(*v), 10) }
func (v *runCountValue) Set(text string) error {
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return err
	}
	if value < 0 {
		return errors.New("must be a nonnegative integer (0 = automatic)")
	}
	*v = runCountValue(value)
	return nil
}
