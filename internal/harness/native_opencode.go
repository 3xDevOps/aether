package harness

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/jsonc"
)

const openCodeNativeLauncherName = "opencode-native-launch.sh"

// V1 discovers project plugins only up to the worktree root; V2 discovers
// ancestor directories through /. These V2 packages therefore participate in
// native discovery before user plugin enablement/removal directives, instead
// of overriding those directives with a highest-priority inline declaration.
const OpenCodeNativeDiscoveryRoot = "/.opencode/plugins"

const openCodeNativeLauncher = `#!/bin/sh
set -eu
coord_dir=$1
shift
version=$("$1" --version)
case "$version" in
  1.*)
    OPENCODE_CONFIG_CONTENT=$(cat "$coord_dir/opencode-native-v1.json")
    export OPENCODE_CONFIG_CONTENT
    ;;
  2.*)
    # Own this run's server; never reuse the member home's detached service.
    executable=$1
    shift
    set -- "$executable" --standalone "$@"
    ;;
  *)
    printf 'Aether: managed OpenCode coordination supports major versions 1 and 2; found %s. Use a custom harness definition for another major version.\n' "$version" >&2
    exit 64
    ;;
esac
exec "$@"
`

func prepareOpenCodeNativeLaunch(dir string, env map[string]string) (NativeLaunch, error) {
	// Do not introduce another route around the member's native plugin opt-out.
	if value := env["OPENCODE_PURE"]; value == "1" || strings.EqualFold(value, "true") {
		return NativeLaunch{ReplacesStatus: true}, nil
	}
	config := make(map[string]json.RawMessage)
	if input := env["OPENCODE_CONFIG_CONTENT"]; input != "" {
		if err := json.Unmarshal(jsonc.Normalize([]byte(input)), &config); err != nil || config == nil {
			return NativeLaunch{}, fmt.Errorf("managed OpenCode coordination requires a JSON or JSONC object in OPENCODE_CONFIG_CONTENT")
		}
	}
	var plugins []json.RawMessage
	if input, ok := config["plugin"]; ok {
		if err := json.Unmarshal(input, &plugins); err != nil || plugins == nil {
			return NativeLaunch{}, fmt.Errorf("managed OpenCode coordination requires a plugin array in OPENCODE_CONFIG_CONTENT")
		}
	}
	// Keep user entries and every unrelated field. Native OpenCode merges this
	// overlay with its own files; Aether never edits any of those files.
	for _, file := range []string{agentstatus.OpenCodePluginName, "opencode-v1.js"} {
		encoded, err := json.Marshal("file://" + path.Join(dir, file))
		if err != nil {
			return NativeLaunch{}, err
		}
		plugins = append(plugins, encoded)
	}
	encoded, err := json.Marshal(plugins)
	if err != nil {
		return NativeLaunch{}, err
	}
	config["plugin"] = encoded
	configBytes, err := json.Marshal(config)
	if err != nil {
		return NativeLaunch{}, err
	}
	files := map[string][]byte{
		openCodeNativeLauncherName:       []byte(openCodeNativeLauncher),
		"opencode-native-v1.json":        configBytes,
		agentstatus.OpenCodePluginName:   agentstatus.OpenCodePlugin,
		agentstatus.OpenCodeV2PluginName: agentstatus.OpenCodeV2Plugin,
	}
	for _, name := range []string{"opencode-v1.js", "opencode-v2.js"} {
		body, err := coordhooks.Files.ReadFile(name)
		if err != nil {
			return NativeLaunch{}, fmt.Errorf("read %s native coordination asset: %w", name, err)
		}
		files[name] = body
	}
	return NativeLaunch{
		Files:          files,
		Wrapper:        []string{"/bin/sh", path.Join(dir, openCodeNativeLauncherName), dir},
		ReplacesStatus: true,
		Reporter:       ReporterFull,
		DiscoveryFiles: map[string]string{
			"aether-mailbox": "opencode-v2.js",
			"aether-status":  agentstatus.OpenCodeV2PluginName,
		},
	}, nil
}
