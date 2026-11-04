package dictation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	containerruntime "github.com/3xDevOps/Aether/internal/runtime"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "dictation" && os.Args[2] == "engine" {
		os.Exit(RunEngine(os.Args[3:]))
	}
	os.Exit(m.Run())
}

// TestRealEngineTranscribesFixture runs the installed engine in Docker on
// testdata/speech.wav, espeak-ng's en-us voice saying "Please run the tests
// and open a pull request." The test binary is the container's entrypoint,
// so build it static: CGO_ENABLED=0 go test -c, then run the binary as root
// with AETHER_TEST_DICTATION_DIR set.
func TestRealEngineTranscribesFixture(t *testing.T) {
	dir := os.Getenv("AETHER_TEST_DICTATION_DIR")
	if dir == "" {
		t.Skip("set AETHER_TEST_DICTATION_DIR to an installed dictation directory, such as /var/lib/aether/dictation")
	}
	wav, err := os.ReadFile("testdata/speech.wav")
	if err != nil {
		t.Fatal(err)
	}
	docker, err := containerruntime.NewDocker()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = docker.Close() }()
	engine, err := NewEngine(dir, docker)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(dir, engine)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	start := time.Now()
	text, err := svc.Transcribe(ctx, "mem-test", wav, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("first clip, engine start included, in %s: %q", time.Since(start).Round(time.Millisecond), text)
	for _, want := range []string{"run the tests", "pull request"} {
		if !strings.Contains(strings.ToLower(text), want) {
			t.Errorf("transcript %q lacks %q", text, want)
		}
	}
	start = time.Now()
	if _, err = svc.Transcribe(ctx, "mem-test", wav, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("second clip, engine warm, in %s", time.Since(start).Round(time.Millisecond))

	pid := enginePID(t)
	if nice := procStat(t, pid, 16); nice != "10" {
		t.Errorf("engine runs at nice %s, want 10", nice)
	}
	if status, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); !strings.Contains(string(status), "\nUid:\t65534\t") {
		t.Errorf("engine is not running as uid 65534:\n%s", status)
	}
	self, _ := os.Readlink("/proc/self/ns/net")
	if ns, _ := os.Readlink(fmt.Sprintf("/proc/%d/ns/net", pid)); ns == self {
		t.Errorf("engine shares the server's network namespace %s", ns)
	}
	if listening(t, "/proc/self/net/tcp", enginePort) {
		t.Errorf("port %d listens in the server's network namespace", enginePort)
	}
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err == nil {
		t.Errorf("engine %d still runs after Close", pid)
	}
}

func enginePID(t *testing.T) int {
	t.Helper()
	comms, _ := filepath.Glob("/proc/[0-9]*/comm")
	for _, path := range comms {
		if b, err := os.ReadFile(path); err == nil && strings.HasPrefix(string(b), "sherpa-onnx-off") {
			pid, _ := strconv.Atoi(filepath.Base(filepath.Dir(path)))
			return pid
		}
	}
	t.Fatal("no sherpa-onnx-offline-websocket-server process")
	return 0
}

func procStat(t *testing.T, pid, field int) string {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+1:]))[field]
}

func listening(t *testing.T, path string, port int) bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(strings.ToUpper(string(b)), fmt.Sprintf(":%04X 00000000:0000 0A", port))
}
