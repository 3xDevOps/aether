package syncd

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestServiceUnitLinux(t *testing.T) {
	f, err := ServiceUnit("linux", "/usr/local/bin/aether", []string{"daemon", "run", "--server", "host:2222", "--repo", "/home/dev/my repo"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Path != ".config/systemd/user/aether-daemon.service" {
		t.Errorf("path = %q", f.Path)
	}
	if want := `ExecStart=/usr/local/bin/aether daemon run --server host:2222 --repo "/home/dev/my repo"`; !strings.Contains(f.Content, want) {
		t.Errorf("unit missing %q:\n%s", want, f.Content)
	}
	for _, want := range []string{"[Unit]", "[Service]", "[Install]", "Restart=on-failure", "WantedBy=default.target"} {
		if !strings.Contains(f.Content, want) {
			t.Errorf("unit missing %q", want)
		}
	}
	if !strings.Contains(f.Activate, "systemctl --user") {
		t.Errorf("activate = %q", f.Activate)
	}
}

func TestSystemdQuote(t *testing.T) {
	cases := map[string]string{
		"plain":      "plain",
		"has space":  `"has space"`,
		`d"q`:        `"d\"q"`,
		`back\slash`: `"back\\slash"`,
		"pct%v":      `"pct%%v"`,
		"dol$v":      `"dol$$v"`,
		"":           `""`,
	}
	for in, want := range cases {
		if got := systemdQuote(in); got != want {
			t.Errorf("systemdQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestServiceUnitDarwin(t *testing.T) {
	f, err := ServiceUnit("darwin", "/usr/local/bin/aether", []string{"daemon", "run", "--server", "host:2222"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Path != "Library/LaunchAgents/com.aether.daemon.plist" {
		t.Errorf("path = %q", f.Path)
	}
	var plist struct{}
	if err := xml.Unmarshal([]byte(f.Content), &plist); err != nil {
		t.Fatalf("plist is not well-formed XML: %v", err)
	}
	for _, want := range []string{
		"<string>com.aether.daemon</string>",
		"<string>/usr/local/bin/aether</string>",
		"<string>daemon</string>",
		"<string>host:2222</string>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(f.Content, want) {
			t.Errorf("plist missing %q", want)
		}
	}
}

func TestServiceUnitWindows(t *testing.T) {
	f, err := ServiceUnit("windows", `C:\bin\aether.exe`, []string{"daemon", "run", "--repo", `C:\my repo`})
	if err != nil {
		t.Fatal(err)
	}
	if f.Path != "aether-daemon.xml" {
		t.Errorf("path = %q", f.Path)
	}
	var task struct{}
	if err := xml.Unmarshal([]byte(f.Content), &task); err != nil {
		t.Fatalf("task XML is not well-formed: %v", err)
	}
	if want := `<Command>C:\bin\aether.exe</Command>`; !strings.Contains(f.Content, want) {
		t.Errorf("task missing %q:\n%s", want, f.Content)
	}
	if want := `<Arguments>daemon run --repo &quot;C:\my repo&quot;</Arguments>`; !strings.Contains(f.Content, want) {
		t.Errorf("task missing %q:\n%s", want, f.Content)
	}
	if !strings.Contains(f.Activate, "schtasks /Create") {
		t.Errorf("activate = %q", f.Activate)
	}
}

func TestServiceUnitUnknownOS(t *testing.T) {
	if _, err := ServiceUnit("plan9", "/bin/aether", nil); err == nil {
		t.Error("want error for unsupported OS")
	}
}
