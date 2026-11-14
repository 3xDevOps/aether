package vm

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestConsoleLogStaysBoundedWhateverTheGuestWrites feeds the drain as Cloud
// Hypervisor does, through the FIFO it opens for writing with truncation,
// and flushes it as the monitor does once Cloud Hypervisor exits.
func TestConsoleLogStaysBoundedWhateverTheGuestWrites(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, consoleLog+".1"), []byte("an earlier boot"), 0o600); err != nil {
		t.Fatal(err)
	}
	flush, err := drainConsole(dir)
	if err != nil {
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
	flush()
	if data, _ := os.ReadFile(filepath.Join(dir, consoleLog)); !bytes.HasSuffix(data, []byte("last line\n")) {
		t.Fatalf("after the flush console.log ends %q, want the last line", data[max(0, len(data)-20):])
	}
	for _, name := range []string{consoleLog, consoleLog + ".1"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || len(data) == 0 || len(data) > consoleLogBytes || bytes.Contains(data, []byte("earlier")) {
			t.Errorf("%s holds %d bytes, %v; want at most %d of this boot", name, len(data), err, consoleLogBytes)
		}
	}
}

func TestConsoleLogResumesAfterARotationFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), consoleLog)
	c, err := openConsoleLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Write(bytes.Repeat([]byte("x"), consoleLogBytes)); err != nil {
		t.Fatal(err)
	}
	// A directory in the way fails the rename, as a host out of room fails
	// the new file.
	if err = os.MkdirAll(filepath.Join(path+".1", "blocker"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Write([]byte("dropped\n")); err == nil {
		t.Fatal("a write past the bound with no way to rotate succeeded")
	}
	if err = os.RemoveAll(path + ".1"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Write([]byte("captured\n")); err != nil {
		t.Fatalf("the write after the host recovered: %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "captured\n" {
		t.Errorf("console.log after recovering = %.40q", data)
	}
}
