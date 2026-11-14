package vm

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestConsoleLogStaysBoundedWhateverTheGuestWrites feeds the drain as Cloud
// Hypervisor does, through the FIFO it opens for writing with truncation.
func TestConsoleLogStaysBoundedWhateverTheGuestWrites(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, consoleLog+".1"), []byte("an earlier boot"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := drainConsole(dir); err != nil {
		t.Fatal(err)
	}
	pipe, err := os.OpenFile(filepath.Join(dir, consolePipe), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	line := bytes.Repeat([]byte("x"), 4095)
	for range 5 * consoleLogBytes / 4096 {
		if _, err = pipe.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pipe.Write([]byte("last line\n")); err != nil {
		t.Fatal(err)
	}
	if err = pipe.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, _ := os.ReadFile(filepath.Join(dir, consoleLog))
		if bytes.HasSuffix(data, []byte("last line\n")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("console.log never got the last line; it holds %d bytes", len(data))
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, name := range []string{consoleLog, consoleLog + ".1"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || len(data) == 0 || len(data) > consoleLogBytes || bytes.Contains(data, []byte("earlier")) {
			t.Errorf("%s holds %d bytes, %v; want at most %d of this boot", name, len(data), err, consoleLogBytes)
		}
	}
}
