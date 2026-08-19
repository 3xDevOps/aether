package syncd

import (
	"fmt"
	"strings"
)

// ServiceName is the user-service identifier across platforms.
const ServiceName = "aether-daemon"

// ServiceFile is a rendered user-level service definition for the daemon.
type ServiceFile struct {
	// Path is where the file belongs, relative to the user home.
	Path string
	// Content is the full file body.
	Content string
	// Activate is the shell command that enables the installed service.
	Activate string
}

// ServiceUnit renders the user-service definition for goos (GOOS values):
// a systemd user unit on linux, a launchd agent plist on darwin, and a
// Scheduled Task definition on windows. exe is the absolute aether binary
// path; args are the `daemon run ...` argv appended to it.
func ServiceUnit(goos, exe string, args []string) (ServiceFile, error) {
	switch goos {
	case "linux":
		return systemdUnit(exe, args), nil
	case "darwin":
		return launchdPlist(exe, args), nil
	case "windows":
		return scheduledTask(exe, args), nil
	default:
		return ServiceFile{}, fmt.Errorf("syncd: no service template for OS %q", goos)
	}
}

// systemdQuote quotes one ExecStart word per systemd.syntax(7).
func systemdQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'\\$%") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`, `%`, `%%`)
	return `"` + r.Replace(s) + `"`
}

func systemdUnit(exe string, args []string) ServiceFile {
	words := make([]string, 0, len(args)+1)
	for _, a := range append([]string{exe}, args...) {
		words = append(words, systemdQuote(a))
	}
	content := `[Unit]
Description=Aether git sync daemon
After=network-online.target

[Service]
ExecStart=` + strings.Join(words, " ") + `
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`
	return ServiceFile{
		Path:     ".config/systemd/user/" + ServiceName + ".service",
		Content:  content,
		Activate: "systemctl --user daemon-reload && systemctl --user enable --now " + ServiceName,
	}
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

func launchdPlist(exe string, args []string) ServiceFile {
	var b strings.Builder
	for _, a := range append([]string{exe}, args...) {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", xmlEscape(a))
	}
	content := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.aether.daemon</string>
	<key>ProgramArguments</key>
	<array>
` + b.String() + `	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
</dict>
</plist>
`
	return ServiceFile{
		Path:     "Library/LaunchAgents/com.aether.daemon.plist",
		Content:  content,
		Activate: "launchctl load -w ~/Library/LaunchAgents/com.aether.daemon.plist",
	}
}

// windowsArgs joins args into one Scheduled Task <Arguments> string,
// double-quoting anything containing spaces or quotes (cmd-style: embedded
// quotes doubled).
func windowsArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\"") {
			a = `"` + strings.ReplaceAll(a, `"`, `""`) + `"`
		}
		quoted = append(quoted, a)
	}
	return strings.Join(quoted, " ")
}

func scheduledTask(exe string, args []string) ServiceFile {
	content := `<?xml version="1.0" encoding="UTF-8"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Aether git sync daemon</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
    </LogonTrigger>
  </Triggers>
  <Settings>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>10</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlEscape(exe) + `</Command>
      <Arguments>` + xmlEscape(windowsArgs(args)) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
	return ServiceFile{
		Path:     ServiceName + ".xml",
		Content:  content,
		Activate: `schtasks /Create /TN "` + ServiceName + `" /XML "%USERPROFILE%\` + ServiceName + `.xml"`,
	}
}
