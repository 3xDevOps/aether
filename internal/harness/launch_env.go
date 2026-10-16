package harness

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/jsonc"
)

// configVariables hold a JSON config object the agent merges over its own,
// so a later overlay adds its keys instead of replacing the whole value.
var configVariables = map[string]bool{"OPENCODE_CONFIG_CONTENT": true}

// listKeys are config arrays an overlay extends rather than replaces, so a
// member's own instructions and plugins stay loaded.
var listKeys = map[string]bool{"instructions": true, "plugin": true}

// MergeEnv copies overlay into dst; overlay's keys win, also inside a
// config variable both set, except that its listKeys arrays are appended.
func MergeEnv(dst, overlay map[string]string) error {
	for key, value := range overlay {
		if base := dst[key]; base != "" && configVariables[key] {
			merged, err := mergeConfig(base, value)
			if err != nil {
				return fmt.Errorf("harness: merge %s: %w", key, err)
			}
			value = merged
		}
		dst[key] = value
	}
	return nil
}

func mergeConfig(base, overlay string) (string, error) {
	var into, from map[string]json.RawMessage
	if err := json.Unmarshal(jsonc.Normalize([]byte(base)), &into); err != nil || into == nil {
		return "", errors.New("the existing value is not a JSON or JSONC object")
	}
	if err := json.Unmarshal(jsonc.Normalize([]byte(overlay)), &from); err != nil || from == nil {
		return "", errors.New("the added value is not a JSON or JSONC object")
	}
	for key, value := range from {
		var base, added []json.RawMessage
		if listKeys[key] && json.Unmarshal(into[key], &base) == nil && json.Unmarshal(value, &added) == nil {
			list, err := json.Marshal(append(base, added...))
			if err != nil {
				return "", err
			}
			value = list
		}
		into[key] = value
	}
	merged, err := json.Marshal(into)
	if err != nil {
		return "", err
	}
	return string(merged), nil
}
