package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/server"
	"github.com/3xDevOps/Aether/internal/serversetup"
)

func TestApplyConfigFilePrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.conf")
	if err := serversetup.WriteConfig(path, map[string]string{"addr": ":2300", "dashboard-port": "9090"}); err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	o := serveFlags(fs)
	if err := fs.Parse([]string{"-addr", ":2400"}); err != nil {
		t.Fatal(err)
	}
	from, err := applyConfigFile(fs, path, false)
	if err != nil {
		t.Fatal(err)
	}
	if *o.addr != ":2400" {
		t.Errorf("addr = %q, want the explicit flag to win", *o.addr)
	}
	if *o.dashboardPort != 9090 {
		t.Errorf("dashboard-port = %d, want the config file value 9090", *o.dashboardPort)
	}
	if !strings.Contains(from, path) {
		t.Errorf("startup suffix = %q, want it to name %s", from, path)
	}
}

// The shipped unit passes --config with the default path, so a fresh
// install that has never written a config file must still start: serve
// treats that one path as optional.
func TestApplyConfigFileMissingOptionalPathIsSilent(t *testing.T) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	o := serveFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	from, err := applyConfigFile(fs, filepath.Join(t.TempDir(), "absent.conf"), true)
	if err != nil {
		t.Fatalf("a missing config at the default path must not fail: %v", err)
	}
	if from != "" {
		t.Errorf("startup suffix = %q, want empty when no config applied", from)
	}
	if *o.addr != server.DefaultAddr {
		t.Errorf("addr = %q, want the flag default", *o.addr)
	}
}

func TestApplyConfigFileMissingRequiredPathFails(t *testing.T) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	serveFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	_, err := applyConfigFile(fs, filepath.Join(t.TempDir(), "absent.conf"), false)
	if err == nil {
		t.Fatal("a config path the operator named that does not exist must be an error")
	}
}

func TestConfigSetValidatesKeyAgainstServeFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.conf")
	var out bytes.Buffer
	err := configSet(&out, path, "dashbord-port", "9090")
	if err == nil {
		t.Fatal("want an error for an option that does not exist")
	}
	if !strings.Contains(err.Error(), "dashbord-port") || !strings.Contains(err.Error(), "dashboard-port") {
		t.Errorf("error = %v, want it to name the typo and list the real options", err)
	}
	if err := configSet(&out, path, "dashboard-port", "not-a-number"); err == nil {
		t.Fatal("want an error for a value the flag rejects")
	}
}

func TestConfigSetPreservesOtherKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.conf")
	if err := serversetup.WriteConfig(path, map[string]string{"addr": ":2300", "data-dir": "/srv/aether"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := configSet(&out, path, "dashboard-port", "9090"); err != nil {
		t.Fatal(err)
	}
	values, err := serversetup.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"addr": ":2300", "data-dir": "/srv/aether", "dashboard-port": "9090"}
	for k, v := range want {
		if values[k] != v {
			t.Errorf("%s = %q, want %q", k, values[k], v)
		}
	}
	if !strings.Contains(out.String(), serversetup.RestartCommand) {
		t.Errorf("output = %q, want the restart reminder", out.String())
	}
}

func TestConfigShowMarksConfiguredOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.conf")
	if err := serversetup.WriteConfig(path, map[string]string{"addr": ":2300"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := configShow(&out, path); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, path) {
		t.Errorf("output does not name the file:\n%s", got)
	}
	for _, want := range []string{"addr", ":2300", "config", "data-dir", "default"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestConfigShowWithNoFileSaysSo(t *testing.T) {
	var out bytes.Buffer
	if err := configShow(&out, filepath.Join(t.TempDir(), "absent.conf")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no config file") {
		t.Errorf("output = %q, want it to say there is no config file", out.String())
	}
}

// TestServeFlagsAreTheOnlyOptionList pins the structural rule: every key the
// config file may hold is a flag `serve` declares, because both come from
// serveFlags.
func TestServeFlagsAreTheOnlyOptionList(t *testing.T) {
	validator := serveFlagSet()
	serveSet := flag.NewFlagSet("serve", flag.ContinueOnError)
	serveFlags(serveSet)

	var serveNames []string
	serveSet.VisitAll(func(f *flag.Flag) { serveNames = append(serveNames, f.Name) })
	if len(serveNames) == 0 {
		t.Fatal("serveFlags declared nothing")
	}
	for _, name := range serveNames {
		if validator.Lookup(name) == nil {
			t.Errorf("option %q is not visible to the config-key validator", name)
		}
	}
}

func TestWriteAndReportPrintsActivationWithoutRunningIt(t *testing.T) {
	dir := t.TempDir()
	unitPath := filepath.Join(dir, "aether-server.service")
	configPath := filepath.Join(dir, "server.conf")
	var out bytes.Buffer
	if err := writeAndReport(&out, unitPath, configPath, map[string]string{"addr": ":2300"}, false); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{unitPath, configPath, serversetup.ActivateCommand, "config set"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	unit, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(unit) != serversetup.DefaultUnit() {
		t.Error("installed unit differs from DefaultUnit()")
	}
	values, err := serversetup.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if values["addr"] != ":2300" {
		t.Errorf("addr = %q, want :2300", values["addr"])
	}
}

func TestWriteAndReportSaysWhenItKeepsAConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "server.conf")
	if err := serversetup.WriteConfig(configPath, map[string]string{"addr": ":2300"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := writeAndReport(&out, filepath.Join(dir, "aether-server.service"), configPath,
		map[string]string{"addr": ":9999"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "--force") {
		t.Errorf("output must say why the config was kept:\n%s", out.String())
	}
}

// install writes the flags it was given and nothing else, so options left
// alone keep tracking the binary's defaults across upgrades.
func TestInstallRecordsOnlyNamedOptions(t *testing.T) {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	_ = fs.String("config", serversetup.DefaultConfigPath, "options file")
	_ = fs.String("unit", serversetup.UnitPath, "unit file")
	_ = fs.Bool("force", false, "overwrite")
	serveFlags(fs)
	if err := fs.Parse([]string{"-config", "/tmp/x.conf", "-addr", ":2300", "-tailnet-auto-join"}); err != nil {
		t.Fatal(err)
	}
	values := installValues(fs)
	want := map[string]string{"addr": ":2300", "tailnet-auto-join": "true"}
	if len(values) != len(want) {
		t.Fatalf("values = %v, want only the named serve options %v", values, want)
	}
	for k, v := range want {
		if values[k] != v {
			t.Errorf("%s = %q, want %q", k, values[k], v)
		}
	}
}

// ServiceDefaults keys must be real serve options, or a fresh install would
// write a config file the server then refuses to start on.
func TestServiceDefaultsApplyToRealServeFlags(t *testing.T) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	o := serveFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if err := serversetup.Apply(fs, serversetup.ServiceDefaults()); err != nil {
		t.Fatalf("ServiceDefaults is not accepted by serve: %v", err)
	}
	if *o.dashboardPort != 8080 {
		t.Errorf("dashboard-port = %d, want the 8080 the packaged unit published", *o.dashboardPort)
	}
	if *o.addr != ":2222" || *o.dataDir != "/var/lib/aether" {
		t.Errorf("addr=%q data-dir=%q, want the packaged unit's values", *o.addr, *o.dataDir)
	}
}
