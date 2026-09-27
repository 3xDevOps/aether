package harness

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestNativeExtensionDiscoveryOptOut(t *testing.T) {
	for _, name := range []string{"omp", "pi"} {
		for _, flag := range []string{"--no-extensions", "-ne"} {
			t.Run(name+flag, func(t *testing.T) {
				p, _ := Lookup(name)
				argv := []string{name, flag, "keep this task"}
				native, err := p.PrepareNativeLaunch("/run/aether", argv, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(native.Files) != 0 || len(native.Env) != 0 || !reflect.DeepEqual(native.Command(argv), argv) {
					t.Fatal("automatic explicit extension bypassed native discovery opt-out")
				}
			})
		}
	}
}

func TestOpenCodeNativeLauncherPreservesProcessContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the native launcher runs in Linux containers, not on Windows clients")
	}
	for _, version := range []string{"1.18.32", "1.18.33", "2.0.18", "2.0.19", "3.0.0"} {
		t.Run(version, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			binary := filepath.Join(dir, "opencode")
			// The launched process records its actual argv/environment, then
			// exits nonzero so the wrapper must preserve exec's exit semantics.
			script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf '%s\\n' \"$TEST_VERSION\"; exit; fi\nprintf '%s\\000' \"$@\" > \"$TEST_RESULT/argv\"\nprintf '%s' \"$OPENCODE_CONFIG_CONTENT\" > \"$TEST_RESULT/config\"\nexit 17\n"
			if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			const originalConfig = `{"model":"kept/model","plugin":["user-plugin",["other-plugin",{"setting":true}]],"plugins":["-aether-mailbox"],"custom":{"url":"https://example.test/x"}}`
			env := map[string]string{"OPENCODE_CONFIG_CONTENT": originalConfig}
			p, _ := Lookup("opencode")
			argv := []string{binary, "--prompt=a task with 'quotes' and $(touch NEVER)", "--title", "keep spaces"}
			native, err := p.PrepareNativeLaunch(dir, argv, env)
			if err != nil {
				t.Fatal(err)
			}
			for name, body := range native.Files {
				if writeErr := os.WriteFile(filepath.Join(dir, name), body, 0o444); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			command := native.Command(argv)
			cmd := exec.CommandContext(t.Context(), command[0], command[1:]...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "TEST_VERSION="+version, "TEST_RESULT="+dir, "OPENCODE_CONFIG_CONTENT="+originalConfig)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("launch result: %v %s", err, output)
			}
			if version == "3.0.0" {
				if exit.ExitCode() != 64 {
					t.Fatalf("unsupported major exit=%d", exit.ExitCode())
				}
				if _, statErr := os.Stat(filepath.Join(dir, "argv")); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatal("unknown major launched with incompatible mounted plugins")
				}
				return
			}
			if exit.ExitCode() != 17 {
				t.Fatalf("child exit was not preserved: %d %s", exit.ExitCode(), output)
			}
			args, err := os.ReadFile(filepath.Join(dir, "argv"))
			if err != nil {
				t.Fatal(err)
			}
			wantArgs := argv[1:]
			if strings.HasPrefix(version, "2.") {
				wantArgs = append([]string{"--standalone"}, wantArgs...)
			}
			if got := strings.Split(strings.TrimSuffix(string(args), "\x00"), "\x00"); !reflect.DeepEqual(got, wantArgs) {
				t.Fatalf("argument boundaries changed: %#v", got)
			}
			if _, statErr := os.Stat(filepath.Join(dir, "NEVER")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatal("prompt was interpreted as shell code")
			}
			config, err := os.ReadFile(filepath.Join(dir, "config"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(version, "2.") {
				if string(config) != originalConfig {
					t.Fatal("V2 received a V1 overlay or modified user config")
				}
				return
			}
			var actual, original map[string]json.RawMessage
			if err := json.Unmarshal(config, &actual); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(originalConfig), &original); err != nil {
				t.Fatal(err)
			}
			var plugins []json.RawMessage
			if err := json.Unmarshal(actual["plugin"], &plugins); err != nil {
				t.Fatal(err)
			}
			if len(plugins) != 4 || string(plugins[0]) != `"user-plugin"` || string(plugins[1]) != `["other-plugin",{"setting":true}]` {
				t.Fatalf("user plugin declarations lost: %s", actual["plugin"])
			}
			var status, mailbox string
			if err := json.Unmarshal(plugins[2], &status); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(plugins[3], &mailbox); err != nil {
				t.Fatal(err)
			}
			if status != "file://"+filepath.Join(dir, "opencode-status.js") || mailbox != "file://"+filepath.Join(dir, "opencode-v1.js") {
				t.Fatalf("V1 selected wrong API assets: %s", actual["plugin"])
			}
			delete(actual, "plugin")
			delete(original, "plugin")
			if !reflect.DeepEqual(actual, original) {
				t.Fatalf("unrelated config changed: %s", config)
			}
		})
	}
}

func TestOpenCodePurePreservesUserLaunch(t *testing.T) {
	p, _ := Lookup("opencode")
	argv := []string{"opencode", "--prompt=user task"}
	env := map[string]string{"OPENCODE_PURE": "1", "OPENCODE_CONFIG_CONTENT": "intentionally native-parsed"}
	native, err := p.PrepareNativeLaunch("/run/aether", argv, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(native.Files) != 0 || len(native.DiscoveryFiles) != 0 || !reflect.DeepEqual(native.Command(argv), argv) || !native.ReplacesStatus || native.Reporter != ReporterNone {
		t.Fatal("plugin opt-out was overridden or reporter still claimed active")
	}
}

func TestOpenCodeNativeInlineJSONC(t *testing.T) {
	p, _ := Lookup("opencode")
	const input = "{ // member configuration\n\"model\":\"user/model\",\"plugin\":[\"user-plugin\",],\"provider\":{\"endpoint\":\"https://example.test/*literal*/\",},}"
	env := map[string]string{"OPENCODE_CONFIG_CONTENT": input}
	native, err := p.PrepareNativeLaunch("/run/aether", []string{"opencode"}, env)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Model    string
		Plugin   []string
		Provider struct{ Endpoint string }
	}
	if err := json.Unmarshal(native.Files["opencode-native-v1.json"], &config); err != nil {
		t.Fatal(err)
	}
	if config.Model != "user/model" || config.Provider.Endpoint != "https://example.test/*literal*/" ||
		len(config.Plugin) != 3 || config.Plugin[0] != "user-plugin" {
		t.Fatalf("member JSONC semantics changed: %+v", config)
	}
	if env["OPENCODE_CONFIG_CONTENT"] != input {
		t.Fatal("V2 original inline config was mutated")
	}
}
