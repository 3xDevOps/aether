package server

import "github.com/3xDevOps/Aether/internal/timeline"

// Session history () is a pure reader over the persisted event log:
// there is no background loop to run, so the builder only attaches the
// handler seam.
func init() {
	registerService("timeline", func(d Deps) (Service, error) {
		if d.Events != nil {
			d.SSH.Services.Timeline = timeline.NewReader(d.Events)
		}
		return nil, nil
	})
}
