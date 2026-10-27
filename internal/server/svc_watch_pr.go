package server

import (
	"errors"

	"github.com/3xDevOps/Aether/internal/github"
	"github.com/3xDevOps/Aether/internal/watch"
)

// Pull request watches: the watch service svc_watch.go builds reads pull
// requests with the GitHub service svc_mirror.go builds, both initialized
// first, before any service starts.
func init() {
	registerService("watch-pr", func(d Deps) (Service, error) {
		watches, ok := d.SSH.Services.Watches.(*watch.Service)
		if !ok {
			return nil, nil
		}
		gh, ok := d.SSH.Services.GitHub.(*github.Service)
		if !ok {
			return nil, errors.New("watch: pull request watches need the GitHub service")
		}
		st, ok := d.Store.(watch.PRStore)
		if !ok {
			return nil, errors.New("watch: the store does not keep pull request snapshots")
		}
		prs, err := watch.NewPRs(watch.PRConfig{GitHub: gh, Store: st, Bus: d.Bus})
		if err != nil {
			return nil, err
		}
		watches.UsePRSource(prs)
		return nil, nil
	})
}
