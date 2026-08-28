package server

// Workspace environments: the env.* control methods drive the scheduler's
// build, verify, swap, and rollback orchestration directly, so this
// builder only attaches the seam.
func init() {
	registerService("environment", func(d Deps) (Service, error) {
		d.SSH.Services.Environments = d.Runs
		return nil, nil
	})
}
