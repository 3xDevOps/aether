//go:build kvm

package vm

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestKVMGuestConsoleFloodStaysBounded has root in a guest write far more
// than consoleLogBytes to the serial console.
func TestKVMGuestConsoleFloodStaysBounded(t *testing.T) {
	e := kvmEngine(t)
	const key = "run-vmconsole"
	rootSession(t, e, key, "docker.io/library/ubuntu:24.04")
	start := time.Now()
	if out, code := vmRun(t, e, key, "head -c 1310720 /dev/zero | tr '\\0' x > /dev/ttyS0 && echo flood-end > /dev/ttyS0 && echo wrote"); code != 0 || !strings.Contains(out, "wrote") {
		t.Fatalf("flood the console: %d %q", code, out)
	}
	t.Logf("1.25 MiB to the serial console took %s", time.Since(start))
	deadline := time.Now().Add(30 * time.Second)
	for {
		data, _ := os.ReadFile(e.path(key, consoleLog))
		if strings.Contains(string(data), "flood-end") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("console.log never got the flood's end; it holds %d bytes", len(data))
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, name := range []string{consoleLog, consoleLog + ".1"} {
		info, err := os.Stat(e.path(key, name))
		if err != nil || info.Size() > consoleLogBytes {
			t.Errorf("%s: %v, %v; want at most %d bytes", name, info, err, consoleLogBytes)
		}
	}
	if out, _ := vmRun(t, e, key, "echo alive"); !strings.Contains(out, "alive") {
		t.Errorf("the VM stopped answering after the flood: %q", out)
	}
}
