package runtime

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"

	"github.com/distribution/reference"
)

// BrowserSharedMemoryBytes is private to each companion, never host IPC. Callers
// reserve this capacity in addition to the companion's admitted memory budget.
const BrowserSharedMemoryBytes int64 = 256 << 20

// BrowserRuntime creates the isolated companion; ordinary Runtime methods own
// start, pause, resume, destruction, and creation-key recovery.
type BrowserRuntime interface {
	CreateBrowser(context.Context, BrowserSpec) (ID, error)
	InspectBrowser(context.Context, ID) (BrowserInfo, error)
}

type BrowserSpec struct {
	RunContainer     ID
	Image            string
	CreationKey      string
	Name             string
	ControlHostPath  string
	CPULimit         float64
	MemoryLimitBytes int64
}

type BrowserInfo struct {
	ContainerID  ID
	RunContainer ID
	CreationKey  string
	State        string
	Image        string
}

func (s BrowserSpec) Validate() error {
	if s.RunContainer == "" || strings.ContainsAny(string(s.RunContainer), "/:\x00\r\n") {
		return errors.New("runtime: browser requires a run container identity")
	}
	if strings.TrimSpace(s.Image) == "" || s.CreationKey == "" {
		return errors.New("runtime: browser image and creation key are required")
	}
	ref, err := reference.ParseAnyReference(s.Image)
	if err != nil {
		return errors.New("runtime: invalid browser image reference")
	}
	if _, digested := ref.(reference.Digested); !digested {
		if tagged, ok := ref.(reference.Tagged); !ok || tagged.Tag() == "latest" {
			return errors.New("runtime: browser image requires a version tag or immutable digest")
		}
	}
	if !filepath.IsAbs(s.ControlHostPath) || filepath.Clean(s.ControlHostPath) == "/" {
		return errors.New("runtime: browser control directory must be an absolute private path")
	}
	if math.IsNaN(s.CPULimit) || math.IsInf(s.CPULimit, 0) || s.CPULimit < 0.001 || s.CPULimit >= float64(math.MaxInt64)/1e9 || s.MemoryLimitBytes <= 0 {
		return errors.New("runtime: browser CPU and memory limits must be positive and finite")
	}
	return nil
}
