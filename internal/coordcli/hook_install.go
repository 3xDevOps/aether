package coordcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/shellquote"
	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

const hookInstallMaxBytes = 256 * 1024

// Inputs describe this invocation, not a guessed parent process or active CLI.
type hookInstallInputs struct {
	home      string
	cwd       string
	env       func(string) string
	lookupEnv func(string) (string, bool)
}

type hookInstallPlan struct {
	id           string
	assets       []string
	paths        []string
	controls     []string
	note         string
	activate     string
	unknownPaths bool
}

type hookInstallControls struct {
	DisableAllHooks    *bool     `json:"disableAllHooks" yaml:"disableAllHooks"`
	AllowManagedOnly   bool      `json:"allowManagedHooksOnly" yaml:"allowManagedHooksOnly"`
	DisabledExtensions []string  `json:"disabledExtensions" yaml:"disabledExtensions"`
	Extensions         []string  `json:"extensions"`
	InlineHooks        *struct{} `toml:"hooks" json:"-" yaml:"-"`
	HooksConfig        struct {
		Enabled  *bool    `json:"enabled"`
		Disabled []string `json:"disabled"`
	} `json:"hooksConfig"`
	Features struct {
		Hooks      *bool `toml:"hooks"`
		CodexHooks *bool `toml:"codex_hooks"`
	} `toml:"features"`
}

type hookInstallEntry struct {
	Type        string             `json:"type"`
	Command     string             `json:"command"`
	Exec        string             `json:"exec"`
	Args        []string           `json:"args"`
	Name        string             `json:"name"`
	Matcher     string             `json:"matcher"`
	Hooks       []hookInstallEntry `json:"hooks"`
	Disabled    bool               `json:"disabled"`
	Async       bool               `json:"async"`
	AsyncRewake bool               `json:"asyncRewake"`
}

type hookInstallDocument struct {
	hookInstallControls
	Version int                           `json:"version"`
	Hooks   map[string][]hookInstallEntry `json:"hooks"`
}

type hookInstallResult struct {
	state   string
	details []string
}

func writeHookInstallation(out io.Writer) error {
	home, homeErr := os.UserHomeDir()
	cwd, cwdErr := os.Getwd()
	if homeErr != nil || cwdErr != nil {
		_, err := fmt.Fprintf(out, "\nHook installation: unverified: %v. Run aether-internal hook file to list copyable assets.\n", errors.Join(homeErr, cwdErr))
		return err
	}
	return writeHookInstallationWithInputs(out, hookInstallInputs{home: home, cwd: cwd, env: os.Getenv, lookupEnv: os.LookupEnv})
}

func hookInstallationPlans(in hookInstallInputs) []hookInstallPlan {
	root := func(key, fallback string) string {
		if value := in.env(key); value != "" {
			return value
		}
		return fallback
	}
	user := func(parts ...string) string { return filepath.Join(append([]string{in.home}, parts...)...) }
	project := func(parts ...string) string { return filepath.Join(append([]string{in.cwd}, parts...)...) }
	claude := root("CLAUDE_CONFIG_DIR", user(".claude"))
	codex := root("CODEX_HOME", user(".codex"))
	copilot := root("COPILOT_HOME", user(".copilot"))
	gemini := filepath.Join(root("GEMINI_CLI_HOME", in.home), ".gemini")
	pi := root("PI_CODING_AGENT_DIR", user(".pi", "agent"))
	ompRoot := user(root("PI_CONFIG_DIR", ".omp"))
	omp := root("PI_CODING_AGENT_DIR", filepath.Join(ompRoot, "agent"))
	profile := root("OMP_PROFILE", in.env("PI_PROFILE"))
	if in.lookupEnv != nil {
		if value, present := in.lookupEnv("OMP_PROFILE"); present {
			profile = value
		}
	}
	ompNote := "CLI --profile/--config/-e and --no-extensions are not observable; confirm the active destination with omp config path."
	if profile != "" && profile != "default" {
		if filepath.Base(profile) == profile && profile != "." && profile != ".." {
			omp = filepath.Join(ompRoot, "profiles", profile, "agent")
		} else {
			ompNote = "Invalid profile environment: active profile path unverified; run omp config path before installing."
		}
	}
	opencode := filepath.Join(root("XDG_CONFIG_HOME", user(".config")), "opencode")
	openPaths := []string{filepath.Join(opencode, "plugins", "aether.js"), project(".opencode", "plugins", "aether.js")}
	if custom := in.env("OPENCODE_CONFIG_DIR"); custom != "" {
		openPaths = append(openPaths, filepath.Join(custom, "plugins", "aether.js"))
	}
	cursorNote := "CLI custom roots, --workspace, managed hooks and headless stop support are unverified."
	if in.env("CURSOR_CONFIG_DIR") != "" || in.env("XDG_CONFIG_HOME") != "" {
		cursorNote += " Custom CLI config environment is set; its effect on hooks.json is undocumented."
	}
	return []hookInstallPlan{
		{id: "claude", assets: []string{"claude.json"}, paths: []string{filepath.Join(claude, "settings.json"), project(".claude", "settings.json"), project(".claude", "settings.local.json")}, controls: []string{"/etc/claude-code/managed-settings.json"}, note: "CLI --settings and managed policy/trust remain unverified.", activate: "Restart Claude Code; review hooks in /hooks and accept workspace trust only through the normal UI. Managed-only policy may prohibit these hooks."},
		{id: "codex", assets: []string{"codex.json"}, paths: []string{filepath.Join(codex, "hooks.json"), project(".codex", "hooks.json")}, controls: []string{filepath.Join(codex, "config.toml"), project(".codex", "config.toml"), "/etc/codex/config.toml"}, note: "Inline TOML hooks, ancestor project layers, plugins, CLI overrides and exact hook trust hashes are not inspected.", activate: "Restart Codex; use /hooks to review and trust these exact definitions (repeat after changes); honor project trust and managed policy. Never bypass hook trust."},
		{id: "copilot", assets: []string{"copilot.json"}, paths: []string{filepath.Join(copilot, "hooks", "aether.json"), project(".github", "hooks", "aether.json")}, controls: []string{filepath.Join(copilot, "settings.json"), project(".claude", "settings.json"), project(".claude", "settings.local.json"), project(".github", "copilot", "settings.json"), project(".github", "copilot", "settings.local.json")}, note: "Other hook filenames, plugins, --config-dir, session trust and enterprise policy are unverified.", activate: "Quit and restart Copilot CLI in the intended trusted repository; resolve trust in its normal UI. Honor disableAllHooks and allowManagedHooksOnly."},
		{id: "gemini", assets: []string{"gemini.json"}, paths: []string{filepath.Join(gemini, "settings.json"), project(".gemini", "settings.json")}, controls: []string{root("GEMINI_CLI_SYSTEM_DEFAULTS_PATH", "/etc/gemini-cli/system-defaults.json"), root("GEMINI_CLI_SYSTEM_SETTINGS_PATH", "/etc/gemini-cli/settings.json")}, note: "GEMINI_CLI_HOME is the parent of .gemini. Folder trust and extension settings remain unverified.", activate: "Fully exit and restart Gemini CLI; inspect /hooks list. Honor folder trust and hooksConfig controls; if intentionally disabled, discuss /hooks enable aether-inbox with the user rather than enabling everything."},
		{id: "cursor", assets: []string{"cursor.json"}, paths: []string{user(".cursor", "hooks.json"), project(".cursor", "hooks.json")}, unknownPaths: in.env("CURSOR_CONFIG_DIR") != "" || in.env("XDG_CONFIG_HOME") != "", note: cursorNote, activate: "Use /quit or /exit and restart Cursor CLI in the intended workspace; respect workspace trust. Observe a supported interactive boundary and inspect /logs; headless stop parity is not guaranteed."},
		{id: "pi", assets: []string{"pi.ts"}, paths: []string{filepath.Join(pi, "extensions", "aether.ts"), project(".pi", "extensions", "aether.ts")}, controls: []string{filepath.Join(pi, "settings.json"), project(".pi", "settings.json")}, note: "Configured/package/-e extension paths, resource exclusions and --no-extensions are unverified; absence here does not prove missing installation.", activate: "Restart pi (or use its /reload), preserving existing extension settings and resource exclusions; inspect extension load errors. Respect project trust through the normal UI. Verify the current root session executes a context boundary."},
		{id: "omp", assets: []string{"omp.ts"}, paths: []string{filepath.Join(omp, "extensions", "aether.ts"), project(".omp", "extensions", "aether.ts")}, controls: []string{filepath.Join(omp, "settings.json"), filepath.Join(omp, "config.yml"), project(".omp", "settings.json"), project(".omp", "config.yml")}, note: ompNote, activate: "Confirm active profile with omp config path, then restart OMP; inspect extension load errors. Honor disabledExtensions: [extension-module:aether] and --no-extensions; do not change them automatically."},
		{id: "opencode", assets: []string{"opencode-v1.js", "opencode-v2.js"}, paths: openPaths, note: "V1/V2 runtime version, explicit plugin config (OPENCODE_CONFIG/OPENCODE_CONFIG_CONTENT), managed policy and additional roots are unverified.", activate: "Check opencode --version and its matching plugin API documentation. Export ONLY opencode-v1.js for V1 OR opencode-v2.js for V2 to aether.js, never both. Restart OpenCode and inspect plugin load errors; copying a V1 export into V2 is not activation."},
	}
}

func writeHookInstallationWithInputs(out io.Writer, in hookInstallInputs) error {
	var text strings.Builder
	text.WriteString("\nHook installation (read-only): configured means matching files on disk, NOT loaded, trusted or executed. Install only your current harness; no process-tree guessing.\n")
	text.WriteString("Bounded check: fixed user/current-directory project paths, regular files <=256 KiB; no ancestor/package scan or config execution. Explicit CLI paths, runtime flags, trust and managed layers may change results.\n")
	text.WriteString("JSON install: export below, then create the destination only if absent; otherwise append only missing hooks.EVENT entries to their arrays, preserving unrelated keys, schema version and hooks. Resolve version conflicts; avoid duplicate user/project installs.\n")
	text.WriteString("Extension install: export the matching source to the shown filename only if absent; inspect/diff existing code before editing. Create parent directories as needed. Never overwrite unrelated/custom code.\n")
	for _, plan := range hookInstallationPlans(in) {
		result, err := inspectHookInstallation(plan)
		if err != nil {
			return err
		}
		fmt.Fprintf(&text, "- %s: %s", plan.id, result.state)
		if len(result.details) > 0 {
			fmt.Fprintf(&text, " (%s)", strings.Join(result.details, "; "))
		}
		fmt.Fprintf(&text, ". %s\n", plan.note)
		fmt.Fprintf(&text, "  Inspect/install destination: %s; project alternative: %s\n", shellquote.Quote(plan.paths[0]), shellquote.Quote(plan.paths[1]))
		for _, asset := range plan.assets {
			fmt.Fprintf(&text, "  aether-internal hook file %s\n", asset)
		}
		fmt.Fprintf(&text, "  %s\n", plan.activate)
	}
	if info, err := os.Stat("/usr/local/bin/aether-internal"); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		text.WriteString("Hook executable: missing/unverified /usr/local/bin/aether-internal (must be a regular executable in the harness environment).\n")
	}
	text.WriteString("Safe self-install: inspect/export/diff, edit only the chosen harness with normal authorization, preserve unrelated config, and do not edit trust records, managed policy or disable flags. Restart/reload as above, then verify a real native boundary; disk checks alone never establish activation.\n")
	text.WriteString("Generic integration: aether-internal hook file generic.sh — adapt to the host's documented context hook; no standard installation path or activation claim.\n")
	text.WriteString("Hooks run only at native lifecycle boundaries, never watch/poll in the background or wake later-idle sessions. They emit a trusted inbox pointer, not peer instructions. Read aether-internal inbox and explicitly ack its token after handling; hook output never acknowledges mail.\n")
	_, err := io.WriteString(out, text.String())
	return err
}

func inspectHookInstallation(plan hookInstallPlan) (hookInstallResult, error) {
	result := hookInstallResult{state: "missing"}
	assets := make([][]byte, 0, len(plan.assets))
	for _, name := range plan.assets {
		data, err := coordhooks.Files.ReadFile(name)
		if err != nil {
			return result, fmt.Errorf("read embedded hook %s: %w", name, err)
		}
		assets = append(assets, data)
	}
	commandHook := strings.HasSuffix(plan.assets[0], ".json")
	var expected hookInstallDocument
	if commandHook {
		if err := json.Unmarshal(assets[0], &expected); err != nil {
			return result, fmt.Errorf("decode embedded hook %s: %w", plan.assets[0], err)
		}
	} else {
		result.state = "unverified"
	}
	found := make(map[string]bool)
	invalid, uncertain, configured, disabled := false, plan.unknownPaths, false, false
	var controls hookInstallControls
	applyControls := func(c hookInstallControls, path string) {
		if c.DisableAllHooks != nil {
			controls.DisableAllHooks = c.DisableAllHooks
		}
		if c.HooksConfig.Enabled != nil {
			controls.HooksConfig.Enabled = c.HooksConfig.Enabled
		}
		if c.Features.Hooks != nil {
			controls.Features.Hooks = c.Features.Hooks
		}
		if c.Features.CodexHooks != nil {
			controls.Features.CodexHooks = c.Features.CodexHooks
		}
		if c.AllowManagedOnly || containsHookSetting(c.HooksConfig.Disabled, "aether-inbox") || containsHookSetting(c.DisabledExtensions, "extension-module:aether") {
			disabled = true
			result.details = append(result.details, "disable/policy entry in "+shellquote.Quote(path))
		}
	}
	// Gemini system defaults precede user/project; system settings override them.
	controlPaths := plan.controls
	if plan.id == "gemini" && len(controlPaths) == 2 {
		controlPaths = append([]string{controlPaths[0]}, append(append([]string{}, plan.paths...), controlPaths[1])...)
	}
	for _, path := range plan.paths {
		data, err := readHookInstallFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			uncertain = true
			result.details = append(result.details, shellquote.Quote(path)+": "+err.Error())
			continue
		}
		if !commandHook {
			matches := false
			for i, asset := range assets {
				if bytes.Equal(bytes.TrimSpace(data), bytes.TrimSpace(asset)) {
					matches, configured = true, true
					result.details = append(result.details, plan.assets[i]+" at "+shellquote.Quote(path))
				}
			}
			if !matches {
				uncertain = true
				result.details = append(result.details, "modified/unknown source at "+shellquote.Quote(path))
			}
			continue
		}
		var doc hookInstallDocument
		if decodeErr := json.Unmarshal(data, &doc); decodeErr != nil {
			invalid = true
			result.details = append(result.details, shellquote.Quote(path)+": "+decodeErr.Error())
			continue
		}
		if expected.Version != 0 && doc.Version != expected.Version {
			invalid = true
			result.details = append(result.details, fmt.Sprintf("%s: hook schema version %d, expected %d", shellquote.Quote(path), doc.Version, expected.Version))
			continue
		}
		if plan.id != "gemini" {
			applyControls(doc.hookInstallControls, path)
		}
		for event, entries := range expected.Hooks {
			if hookEventInstalled(doc.Hooks[event], entries) {
				found[event] = true
			}
		}
	}
	for _, path := range controlPaths {
		data, err := readHookInstallFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		var c hookInstallControls
		if err == nil {
			switch filepath.Ext(path) {
			case ".toml":
				err = toml.Unmarshal(data, &c)
			case ".yml", ".yaml":
				err = yaml.Unmarshal(data, &c)
			default:
				err = json.Unmarshal(stripHookJSONComments(data), &c)
			}
		}
		if err != nil {
			uncertain = true
			result.details = append(result.details, "controls at "+shellquote.Quote(path)+": "+err.Error())
			continue
		}
		applyControls(c, path)
		if plan.id == "codex" && c.InlineHooks != nil {
			uncertain = true
			result.details = append(result.details, "inline hook definitions/trust unverified at "+shellquote.Quote(path))
		}
		if plan.id == "pi" {
			for _, extension := range c.Extensions {
				if strings.HasPrefix(extension, "!") || strings.HasPrefix(extension, "-") {
					uncertain = true
					result.details = append(result.details, "resource exclusions require review at "+shellquote.Quote(path))
					break
				}
			}
		}
	}
	if commandHook {
		var missing []string
		for event := range expected.Hooks {
			if !found[event] {
				missing = append(missing, event)
			}
		}
		sort.Strings(missing)
		configured = len(missing) == 0
		if !configured {
			result.details = append(result.details, "missing registrations: "+strings.Join(missing, ","))
		}
	} else if !configured && !uncertain {
		result.details = append(result.details, "no packaged asset at checked paths; explicit/plugin paths not inspected")
	}
	if configured {
		result.state = "configured"
	}
	if uncertain {
		result.state = "unverified"
	}
	if invalid {
		result.state = "invalid"
	}
	if (controls.DisableAllHooks != nil && *controls.DisableAllHooks) || (controls.HooksConfig.Enabled != nil && !*controls.HooksConfig.Enabled) || (controls.Features.Hooks != nil && !*controls.Features.Hooks) || (controls.Features.Hooks == nil && controls.Features.CodexHooks != nil && !*controls.Features.CodexHooks) {
		disabled = true
		result.details = append(result.details, "disabled by inspected settings; runtime overrides unverified")
	}
	if disabled {
		result.state = "disabled"
	}
	return result, nil
}

func containsHookSetting(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// Match complete registrations, never an arbitrary mention in a comment or command.
func hookEventInstalled(actual, expected []hookInstallEntry) bool {
	flatten := func(entries []hookInstallEntry) []hookInstallEntry {
		var commands []hookInstallEntry
		for _, entry := range entries {
			if entry.Matcher != "" && entry.Matcher != "*" {
				continue
			}
			if entry.Disabled || entry.Async || entry.AsyncRewake {
				continue
			}
			if entry.Hooks != nil {
				commands = append(commands, entry.Hooks...)
			} else {
				commands = append(commands, entry)
			}
		}
		return commands
	}
	available := flatten(actual)
	for _, want := range flatten(expected) {
		found := false
		for _, got := range available {
			if !got.Disabled && !got.Async && !got.AsyncRewake && got.Type == want.Type && got.Command == want.Command && got.Exec == want.Exec && reflect.DeepEqual(got.Args, want.Args) && got.Name == want.Name && (got.Matcher == "" || got.Matcher == "*") {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return len(expected) > 0
}

func readHookInstallFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > hookInstallMaxBytes {
		return nil, fmt.Errorf("not a regular configuration file within size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, hookInstallMaxBytes+1))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if len(data) > hookInstallMaxBytes {
		return nil, fmt.Errorf("configuration exceeds size limit")
	}
	return data, err
}

// Copilot settings are JSONC. Preserve quoted strings and newlines while removing
// comments and trailing commas; ordinary JSON decoding still validates structure.
func stripHookJSONComments(data []byte) []byte {
	clean := bytes.Clone(data)
	quoted, escaped := false, false
	for i := 0; i < len(clean); i++ {
		c := clean[i]
		if quoted {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		if c == '"' {
			quoted = true
			continue
		}
		if c != '/' || i+1 >= len(clean) {
			continue
		}
		if clean[i+1] == '/' {
			for ; i < len(clean) && clean[i] != '\n'; i++ {
				clean[i] = ' '
			}
		} else if clean[i+1] == '*' {
			clean[i], clean[i+1] = ' ', ' '
			i += 2
			closed := false
			for ; i < len(clean); i++ {
				if clean[i] == '*' && i+1 < len(clean) && clean[i+1] == '/' {
					clean[i], clean[i+1] = ' ', ' '
					i++
					closed = true
					break
				}
				if clean[i] != '\n' && clean[i] != '\r' {
					clean[i] = ' '
				}
			}
			if !closed {
				return data // Let the JSON decoder reject an unterminated comment.
			}
		}
	}
	quoted, escaped = false, false
	for i, c := range clean {
		if quoted {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case ',':
			j := i + 1
			for j < len(clean) && (clean[j] == ' ' || clean[j] == '\n' || clean[j] == '\r' || clean[j] == '\t') {
				j++
			}
			if j < len(clean) && (clean[j] == '}' || clean[j] == ']') {
				clean[i] = ' '
			}
		}
	}
	return clean
}
