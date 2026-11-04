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
}

func (c *slowContainers) StartDictation(context.Context, containerruntime.DictationSpec) (containerruntime.ID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.starts++
	return "engine", nil
}

func (c *slowContainers) DictationState(context.Context, containerruntime.ID) (containerruntime.DictationState, error) {
	return containerruntime.DictationState{Running: true}, nil
}

func (c *slowContainers) RemoveDictation(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removes++
	return nil
}

// A client that gives up while the model loads leaves the engine loading,
// so retrying does not start it over each time.
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
	engine := &sidecar{dir: dir, wrapper: "/bin/true", containers: containers, memory: func() (uint64, error) { return minMemory, nil }}
	for range 2 {
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		_, err = engine.Transcribe(ctx, make([]byte, 320))
		cancel()
		if err == nil || !strings.Contains(err.Error(), "request ended while the engine was starting") {
			t.Fatalf("Transcribe = %v; want the request to end while the engine starts", err)
		}
	}
	if containers.starts != 1 || containers.removes != 0 {
		t.Fatalf("starts %d, removes %d; want one start and no removal", containers.starts, containers.removes)
	}
	if err = engine.Close(); err != nil || containers.removes != 1 {
		t.Fatalf("Close = %v with %d removals; want the engine removed", err, containers.removes)
	}
}
