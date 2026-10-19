package server

import (
	"errors"

	"github.com/3xDevOps/Aether/internal/memory"
)

// newMemory builds team memory before any service builder runs, so every
// builder that serves it shares one: coord on run sockets, sshd to members.
func newMemory(d Deps) (*memory.Service, error) {
	notes, ok := d.Store.(memory.Store)
	if !ok {
		return nil, errors.New("team memory needs a store with the memory table")
	}
	return memory.New(memory.Config{Store: notes, Bus: d.Bus, Log: d.Events})
}

func init() {
	registerService("memory", func(d Deps) (Service, error) {
		d.SSH.Services.Memory = d.Memory
		return nil, nil
	})
}
