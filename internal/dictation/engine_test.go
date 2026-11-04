package dictation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	containerruntime "github.com/3xDevOps/Aether/internal/runtime"
)

// slowContainers starts an engine that never gets ready.
type slowContainers struct {
	mu              sync.Mutex
	starts, removes int
	owners          []string
}

func (c *slowContainers) StartDictation(_ context.Context, spec containerruntime.DictationSpec) (containerruntime.ID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.starts++
	c.owners = append(c.owners, spec.Owner)
	return "engine", nil
}

func (c *slowContainers) DictationState(context.Context, containerruntime.ID) (containerruntime.DictationState, error) {
	return containerruntime.DictationState{Running: true}, nil
}

func (c *slowContainers) RemoveDictation(_ context.Context, owner string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removes++
	c.owners = append(c.owners, owner)
	return nil
}

// A client that gives up while the model loads leaves the engine loading,
// so retrying does not start it over each time; a new install replaces it.
// Only this install's container is ever started or removed.
func TestEngineKeepsLoadingWhenARequestEnds(t *testing.T) {
	dir, err := os.MkdirTemp("", "dict")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err = os.WriteFile(filepath.Join(dir, manifestName), []byte(`{"provider":"cpu","precision":"int8"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	containers := &slowContainers{}
	engine := &sidecar{dir: dir, wrapper: "/bin/true", containers: containers, memory: func() (uint64, error) { return minMemory, nil }, owner: Owner(dir)}
	transcribe := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		if _, terr := engine.Transcribe(ctx, make([]byte, 320)); terr == nil || !strings.Contains(terr.Error(), "request ended while the engine was starting") {
			t.Fatalf("Transcribe = %v; want the request to end while the engine starts", terr)
		}
	}
	transcribe()
	transcribe()
	if containers.starts != 1 || containers.removes != 0 {
		t.Fatalf("starts %d, removes %d; want one start and no removal", containers.starts, containers.removes)
	}
	if err = os.WriteFile(filepath.Join(dir, manifestName), []byte(`{"provider":"cuda","precision":"fp16"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	transcribe()
	if containers.starts != 2 || containers.removes != 1 {
		t.Fatalf("after a reinstall: starts %d, removes %d; want the engine replaced", containers.starts, containers.removes)
	}
	if err = engine.Close(); err != nil || containers.removes != 2 {
		t.Fatalf("Close = %v with %d removals; want the engine removed", err, containers.removes)
	}
	for _, owner := range containers.owners {
		if owner != Owner(dir) {
			t.Fatalf("owners %v; want only %s", containers.owners, Owner(dir))
		}
	}
}
