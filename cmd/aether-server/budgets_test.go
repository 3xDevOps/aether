package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/serversetup"
)

func TestBudgetFlagsRejectInvalidValuesBeforeConfigWrites(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"run-cpus", "-1"}, {"run-cpus", "NaN"}, {"run-cpus", "+Inf"}, {"run-cpus", "-Inf"}, {"run-cpus", "1e300"},
		{"run-cpus", "1e-300"},
		{"run-memory", "-1"}, {"run-memory", "NaN"}, {"run-memory", "9223372036854775808"},
		{"run-pids", "-1"}, {"run-pids", "Inf"}, {"run-pids", "1.5"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			fs := serveFlagSet()
			if err := fs.Parse([]string{"--" + tc.key + "=" + tc.value}); err == nil {
				t.Fatal("invalid flag accepted")
			}
			path := filepath.Join(t.TempDir(), "server.conf")
			if err := serversetup.WriteConfig(path, map[string]string{"addr": ":2300"}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if setErr := configSet(&out, path, tc.key, tc.value); setErr == nil || !strings.Contains(setErr.Error(), tc.key) {
				t.Fatalf("config set accepted invalid budget: %v", setErr)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("rejected config changed the file: %v", err)
			}
			if err := serversetup.WriteConfig(path, map[string]string{tc.key: tc.value}); err != nil {
				t.Fatal(err)
			}
			if _, err := applyConfigFile(serveFlagSet(), path, false); err == nil {
				t.Fatal("edited config accepted invalid budget")
			}
		})
	}
}

func TestBudgetConfigPrecedenceAndAutomaticReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.conf")
	var out bytes.Buffer
	for key, value := range map[string]string{"run-cpus": "3.5", "run-memory": "17179869184", "run-pids": "8192", "min-free-disk": "-1"} {
		if err := configSet(&out, path, key, value); err != nil {
			t.Fatal(err)
		}
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	o := serveFlags(fs)
	if err := fs.Parse([]string{"--run-cpus=0", "--run-memory=0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyConfigFile(fs, path, false); err != nil {
		t.Fatal(err)
	}
	if *o.runCPUs != 0 || *o.runMemory != 0 || *o.runPids != 8192 || *o.minFreeDisk != -1 {
		t.Fatalf("explicit automatic reset lost to config: cpus=%v memory=%v pids=%v disk=%v", *o.runCPUs, *o.runMemory, *o.runPids, *o.minFreeDisk)
	}
	out.Reset()
	if err := configShow(&out, path); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"run-cpus", "3.5", "run-memory", "17179869184", "run-pids", "8192"} {
		if !strings.Contains(out.String(), value) {
			t.Fatalf("config show omitted %q: %s", value, out.String())
		}
	}
}
