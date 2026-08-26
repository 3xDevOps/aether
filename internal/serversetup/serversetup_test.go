package serversetup

import (
	"flag"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseIgnoresCommentsAndBlankLines(t *testing.T) {
	values, err := Parse(strings.NewReader(`# the listen address

addr = :2222

  # indented comment
dashboard-port = 8080
tailnet-auto-join=true
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"addr": ":2222", "dashboard-port": "8080", "tailnet-auto-join": "true"}
	if !maps.Equal(values, want) {
		t.Errorf("values = %v, want %v", values, want)
	}
}

func TestParseRoundTripsThroughRender(t *testing.T) {
	want := map[string]string{
		"addr":              ":2200",
		"data-dir":          "/var/lib/aether",
		"dashboard-port":    "8080",
		"tailnet-auto-join": "false",
	}
	rendered := Render(want)
	got, err := Parse(strings.NewReader(rendered))
	if err != nil {
		t.Fatalf("reparse rendered config: %v\n%s", err, rendered)
	}
	if !maps.Equal(got, want) {
		t.Errorf("round trip = %v, want %v", got, want)
	}
}

func TestRenderIsDeterministicallyOrdered(t *testing.T) {
	body := Render(map[string]string{"data-dir": "/var/lib/aether", "addr": ":2222", "dashboard-port": "8080"})
	addr := strings.Index(body, "addr = ")
	dashboard := strings.Index(body, "dashboard-port = ")
	dataDir := strings.Index(body, "data-dir = ")
	if !(addr < dashboard && dashboard < dataDir) {
		t.Errorf("keys are not sorted:\n%s", body)
	}
	if !strings.Contains(body, "operator-owned") {
		t.Errorf("header does not say the file is operator-owned:\n%s", body)
	}
}

func TestParseMalformedLineReportsLineNumber(t *testing.T) {
	_, err := Parse(strings.NewReader("addr = :2222\n\n# fine\nnot a config line\n"))
	if err == nil {
		t.Fatal("want an error for a line with no '='")
	}
	if !strings.Contains(err.Error(), "line 4") {
		t.Errorf("error = %v, want it to name line 4", err)
	}
}

func TestLoadMissingFileIsEmptyAndNotAnError(t *testing.T) {
	values, err := Load(filepath.Join(t.TempDir(), "absent.conf"))
	if err != nil {
		t.Fatalf("missing config must not be an error: %v", err)
	}
	if len(values) != 0 {
		t.Errorf("values = %v, want empty", values)
	}
}

func TestLoadReadsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.conf")
	if err := WriteConfig(path, map[string]string{"addr": ":2300"}); err != nil {
		t.Fatal(err)
	}
	values, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if values["addr"] != ":2300" {
		t.Errorf("addr = %q, want :2300", values["addr"])
	}
}

// testFlags mirrors the shape of the real `serve` set: enough option kinds
// that Apply exercises string, int, and bool parsing.
func testFlags() (*flag.FlagSet, *string, *int, *bool) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", ":2222", "SSH listen address")
	port := fs.Int("dashboard-port", 0, "dashboard port")
	autoJoin := fs.Bool("tailnet-auto-join", false, "auto-join")
	return fs, addr, port, autoJoin
}

func TestApplySetsFlagsNotPassedExplicitly(t *testing.T) {
	fs, addr, port, autoJoin := testFlags()
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	err := Apply(fs, map[string]string{"addr": ":2300", "dashboard-port": "9090", "tailnet-auto-join": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if *addr != ":2300" || *port != 9090 || !*autoJoin {
		t.Errorf("addr=%q port=%d autoJoin=%v, want :2300 9090 true", *addr, *port, *autoJoin)
	}
}

func TestApplyExplicitFlagBeatsConfigFile(t *testing.T) {
	fs, addr, port, _ := testFlags()
	if err := fs.Parse([]string{"-addr", ":2400"}); err != nil {
		t.Fatal(err)
	}
	if err := Apply(fs, map[string]string{"addr": ":2300", "dashboard-port": "9090"}); err != nil {
		t.Fatal(err)
	}
	if *addr != ":2400" {
		t.Errorf("addr = %q, want the explicit flag :2400 to win", *addr)
	}
	if *port != 9090 {
		t.Errorf("dashboard-port = %d, want the config value 9090 for a flag not passed", *port)
	}
}

func TestApplyRejectsUnknownKey(t *testing.T) {
	fs, addr, _, _ := testFlags()
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	err := Apply(fs, map[string]string{"addr": ":2300", "dashbord-port": "9090"})
	if err == nil {
		t.Fatal("want an error naming the typo'd key")
	}
	if !strings.Contains(err.Error(), "dashbord-port") {
		t.Errorf("error = %v, want it to name dashbord-port", err)
	}
	if *addr != ":2222" {
		t.Errorf("addr = %q, want nothing applied when a key is unknown", *addr)
	}
}

func TestApplyRejectsUnparseableValue(t *testing.T) {
	fs, _, _, _ := testFlags()
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	err := Apply(fs, map[string]string{"dashboard-port": "eighty-eighty"})
	if err == nil || !strings.Contains(err.Error(), "dashboard-port") {
		t.Fatalf("err = %v, want it to name dashboard-port", err)
	}
}

// TestPackagedUnitMatchesDefaultUnit is the single-source-of-truth invariant:
// scripts/deploy.sh installs the packaged file while `aether-server install`
// writes DefaultUnit(), so the two must be byte-identical.
func TestPackagedUnitMatchesDefaultUnit(t *testing.T) {
	path := filepath.Join("..", "..", "packaging", "systemd", "aether-server.service")
	packaged, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(packaged) != DefaultUnit() {
		t.Errorf("%s differs from DefaultUnit()\npackaged:\n%s\nDefaultUnit():\n%s", path, packaged, DefaultUnit())
	}
}

func TestDefaultUnitKeepsOperationalDirectives(t *testing.T) {
	unit := DefaultUnit()
	for _, want := range []string{
		"Requires=docker.service",
		"ExecStart=/usr/local/bin/aether-server serve --config " + DefaultConfigPath,
		"EnvironmentFile=-/etc/aether/aether-server.env",
		"StateDirectory=aether",
		"StateDirectoryMode=0700",
		"Restart=on-failure",
		"KillSignal=SIGTERM",
		"TimeoutStopSec=90",
		"LimitNOFILE=65536",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit is missing %q", want)
		}
	}
}

// Moving the options out of ExecStart must not quietly change what a fresh
// install does. The old unit hardcoded --dashboard-port 8080, while the bare
// `serve` default denies the dashboard, so an install that seeded the config
// from flag defaults alone would silently drop it.
func TestInstallSeedsThePackagedServicePosture(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "server.conf")
	if _, err := Install(filepath.Join(dir, "u.service"), configPath, nil, false); err != nil {
		t.Fatal(err)
	}
	values, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"addr": ":2222", "dashboard-port": "8080", "data-dir": "/var/lib/aether"}
	if !maps.Equal(values, want) {
		t.Errorf("seeded config = %v, want the posture the old ExecStart hardcoded %v", values, want)
	}
}

func TestInstallLayersGivenValuesOverServiceDefaults(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "server.conf")
	if _, err := Install(filepath.Join(dir, "u.service"), configPath,
		map[string]string{"addr": ":2300", "tailnet-auto-join": "true"}, false); err != nil {
		t.Fatal(err)
	}
	values, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"addr":              ":2300",
		"dashboard-port":    "8080",
		"data-dir":          "/var/lib/aether",
		"tailnet-auto-join": "true",
	}
	if !maps.Equal(values, want) {
		t.Errorf("config = %v, want %v", values, want)
	}
}

// ServiceDefaults must survive a Render/Parse round trip unchanged, since
// that is exactly what Install does with it.
func TestServiceDefaultsRoundTrip(t *testing.T) {
	got, err := Parse(strings.NewReader(Render(ServiceDefaults())))
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(got, ServiceDefaults()) {
		t.Errorf("round trip = %v, want %v", got, ServiceDefaults())
	}
}

func TestInstallWritesUnitAndConfig(t *testing.T) {
	dir := t.TempDir()
	unitPath := filepath.Join(dir, "systemd", "aether-server.service")
	configPath := filepath.Join(dir, "aether", "server.conf")

	res, err := Install(unitPath, configPath, map[string]string{"addr": ":2300"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.ConfigSkipped {
		t.Error("first install must write the config file")
	}
	unit, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(unit) != DefaultUnit() {
		t.Error("installed unit differs from DefaultUnit()")
	}
	values, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if values["addr"] != ":2300" {
		t.Errorf("addr = %q, want :2300", values["addr"])
	}
}

func TestInstallKeepsExistingConfigUnlessForced(t *testing.T) {
	dir := t.TempDir()
	unitPath := filepath.Join(dir, "aether-server.service")
	configPath := filepath.Join(dir, "server.conf")
	if err := WriteConfig(configPath, map[string]string{"addr": ":2300"}); err != nil {
		t.Fatal(err)
	}

	res, err := Install(unitPath, configPath, map[string]string{"addr": ":9999"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ConfigSkipped {
		t.Error("an existing config file must be reported as skipped")
	}
	values, _ := Load(configPath)
	if values["addr"] != ":2300" {
		t.Errorf("addr = %q, want the operator's :2300 left alone", values["addr"])
	}

	if _, err := Install(unitPath, configPath, map[string]string{"addr": ":9999"}, true); err != nil {
		t.Fatal(err)
	}
	values, _ = Load(configPath)
	if values["addr"] != ":9999" {
		t.Errorf("addr = %q, want --force to overwrite", values["addr"])
	}
}
