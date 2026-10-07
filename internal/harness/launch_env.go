package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/3xDevOps/Aether/internal/jsonc"
)

// configVariables hold a JSON config object the agent merges over its own,
// so a later overlay adds its keys instead of replacing the whole value.
var configVariables = map[string]bool{"OPENCODE_CONFIG_CONTENT": true}

// MergeEnv copies overlay into dst; overlay's keys win, also inside a
// config variable both set.
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
	maps.Copy(into, from)
	merged, err := json.Marshal(into)
	if err != nil {
		return "", err
	}
	return string(merged), nil
}
