// Package serversetup owns the operator-facing installation surface of
// aether-server: the system config file that holds the `serve` options and
// the systemd unit that runs the binary against it. Options live in the
// config file rather than in the unit's ExecStart so that they survive
// binary updates and unit reinstalls.
package serversetup

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
)

// DefaultConfigPath is the system config file read by `aether-server serve`.
const DefaultConfigPath = "/etc/aether/server.conf"

// Parse reads a config body: "key = value" lines, "#" comments, blank lines
// ignored. Keys are exactly the `serve` flag names without their dashes.
// A value runs to the end of its line, so "#" inside a value is not a
// comment. A malformed line is reported with its line number.
func Parse(r io.Reader) (map[string]string, error) {
	values := make(map[string]string)
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("serversetup: line %d: want \"key = value\", got %q", n, line)
		}
		values[key] = strings.TrimSpace(value)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("serversetup: read config: %w", err)
	}
	return values, nil
}

// Load parses the config file at path. A missing file yields an empty map
// and no error: a default install carries no config file and must still run.
func Load(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("serversetup: open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle
	values, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return values, nil
}

// Apply sets values on set, skipping every flag the operator already passed
// explicitly. It must be called after set.Parse: explicitness is read from
// set.Visit, which visits only flags actually set. That yields the precedence
// explicit flag > config file > flag default without naming a single option
// here. An unrecognised key is an error, so a typo is never silently ignored,
// and keys are checked before anything is applied.
func Apply(set *flag.FlagSet, values map[string]string) error {
	explicit := make(map[string]bool)
	set.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	keys := slices.Sorted(maps.Keys(values))
	for _, key := range keys {
		if set.Lookup(key) == nil {
			return fmt.Errorf("serversetup: unknown option %q", key)
		}
	}
	for _, key := range keys {
		if explicit[key] {
			continue
		}
		if err := set.Set(key, values[key]); err != nil {
			return fmt.Errorf("serversetup: option %q: %w", key, err)
		}
	}
	return nil
}

// Render writes values back out as a config file body, keys sorted so the
// file is stable across rewrites.
func Render(values map[string]string) string {
	var b strings.Builder
	b.WriteString("# Options for the aether-server service.\n")
	b.WriteString("#\n" +
		"# This file is operator-owned: binary updates and unit reinstalls never\n" +
		"# rewrite it. Keys are exactly the `aether-server serve` flag names\n" +
		"# without their leading dashes, and a flag passed on the command line\n" +
		"# wins over the value here.\n" +
		"#\n" +
		"# Change it with `aether-server config set <key> <value>`, then\n" +
		"# `systemctl restart aether-server` to apply.\n\n")
	for _, key := range slices.Sorted(maps.Keys(values)) {
		fmt.Fprintf(&b, "%s = %s\n", key, values[key])
	}
	return b.String()
}
