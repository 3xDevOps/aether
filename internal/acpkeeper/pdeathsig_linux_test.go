package acpkeeper

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A keeper helper runs Serve or ServeTerm while goroutines that lock their
// OS thread and return end one thread after another.
func TestMain(m *testing.M) {
	if entry := os.Getenv("ACPKEEPER_TEST_ENTRY"); entry != "" {
		socket, argv := os.Args[1], os.Args[2:]
		go func() {
			for {
				done := make(chan struct{})
				go func() {
					defer close(done)
					runtime.LockOSThread()
				}()
				<-done
				time.Sleep(time.Millisecond)
			}
		}()
		// The main thread never ends, so the keeper runs off it.
		runtime.LockOSThread()
		code := make(chan int)
		go func() {
			if entry == "term" {
				code <- ServeTerm(socket, "", "", 80, 24, argv)
			}
			code <- Serve(socket, "", argv)
		}()
		os.Exit(<-code)
	}
	os.Exit(m.Run())
}

func TestEndedKeeperThreadsDoNotKillTheChild(t *testing.T) {
	for _, entry := range []string{"acp", "term"} {
		t.Run(entry, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "pid")
			cmd := exec.Command(os.Args[0], filepath.Join(dir, "acp", "k", "sock"), "sh", "-c", `echo $$ >"$0"; exec sleep 60`, pidFile)
			cmd.Env = append(os.Environ(), "ACPKEEPER_TEST_ENTRY="+entry, "GOMAXPROCS=1")
			cmd.Stdin = strings.NewReader("token\n")
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			})
			if line, err := bufio.NewReader(out).ReadString('\n'); line != "ready\n" {
				t.Fatalf("keeper printed %q, %v", line, err)
			}
			var pid int
			for deadline := time.Now().Add(5 * time.Second); pid == 0; time.Sleep(10 * time.Millisecond) {
				raw, _ := os.ReadFile(pidFile)
				pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
				if pid == 0 && time.Now().After(deadline) {
					t.Fatal("the child never wrote its pid")
				}
			}
			time.Sleep(time.Second)
			if err := syscall.Kill(pid, 0); err != nil {
				t.Fatalf("the child died while the keeper ran: %v", err)
			}
			if cmd.ProcessState != nil {
				t.Fatal("the keeper exited")
			}
		})
	}
}
