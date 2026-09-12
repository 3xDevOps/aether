//go:build windows

package main

import "golang.org/x/sys/windows"

// openBrowser hands url to the shell's registered handler. ShellExecute is
// where `rundll32 url.dll,FileProtocolHandler` ends up anyway; calling it
// directly keeps a rundll32 child out of the client's run path, which
// antivirus heuristics score as proxy execution. A desktop session that
// refuses the URL leaves the dashboard address on stdout, so the failure is
// recoverable and not worth reporting twice.
func openBrowser(url string) {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return
	}
	target, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return
	}
	_ = windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}
