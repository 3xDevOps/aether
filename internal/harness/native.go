package harness

import (
	"fmt"
	"path"
	"slices"

	"github.com/3xDevOps/Aether/internal/coordhooks"
)

// NativeLaunch is one run's server-owned native mailbox integration. It is
// separate from lifecycle status reporting and never changes persistent config.
type NativeLaunch struct {
	Files    map[string][]byte
	Args     []string
	Env      map[string]string
	Wrapper  []string
	Reporter Reporter
	// DiscoveryFiles maps native package IDs to flat staged entrypoint files.
	// The scheduler gives each package a run-unique, read-only discovery path.
	DiscoveryFiles map[string]string
	ReplacesStatus bool
}

// Command preserves the original argument boundaries, including prompts that
// contain shell syntax. The OpenCode V2 wrapper additionally requests a private
// server with --standalone before exec, keeping its lifetime within this run.
func (n NativeLaunch) Command(argv []string) []string {
	argv = append(argv, n.Args...)
	if len(n.Wrapper) == 0 {
		return argv
	}
	return append(slices.Clone(n.Wrapper), argv...)
}

// PrepareNativeLaunch is called only for coordinated, task-bearing interactive
// runs. The registry capability, not the profile name alone, authorizes loading.
func (p Profile) PrepareNativeLaunch(dir string, argv []string, env map[string]string) (NativeLaunch, error) {
	if !p.NativeCoordination || dir == "" || len(argv) == 0 {
		return NativeLaunch{}, nil
	}
	switch p.Name {
	case "omp", "pi":
		for _, arg := range argv[1:] {
			if arg == "--" {
				break
			}
			if arg == "--no-extensions" || arg == "-ne" {
				return NativeLaunch{}, nil
			}
		}
		body, err := coordhooks.Files.ReadFile(p.Name + ".ts")
		if err != nil {
			return NativeLaunch{}, fmt.Errorf("read %s native coordination asset: %w", p.Name, err)
		}
		extension := path.Join(dir, "aether.ts")
		if p.Name == "omp" {
			// OMP's explicit-file path bypasses disabledExtensions, whereas
			// its directory loader filters extension-module:aether normally.
			extension = dir
		}
		return NativeLaunch{
			Files: map[string][]byte{"aether.ts": body},
			Args:  []string{"-e", extension},
			Env:   map[string]string{"AETHER_MANAGED_NATIVE_WAKE": path.Join(dir, "aether.ts")},
		}, nil
	case "opencode":
		return prepareOpenCodeNativeLaunch(dir, env)
	default:
		return NativeLaunch{}, nil
	}
}
