package ptyhost

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Lower only the helper process's descriptor limit: parallel package tests and
// the race runtime in the parent must never inherit a process-global test cap.
func TestLegacyReplayUnderDescriptorBudget(t *testing.T) {
	const helper = "AETHER_TEST_LEGACY_REPLAY_FD_HELPER"
	if os.Getenv(helper) != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestLegacyReplayUnderDescriptorBudget$")
		cmd.Env = append(os.Environ(), helper+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bounded-descriptor helper: %v\n%s", err, output)
		}
		return
	}
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	if limit.Cur > 64 {
		limit.Cur = 64
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing", "v1", "stale"} {
		t.Run(mode, func(t *testing.T) {
			h, dir := newTestHost(t)
			path := filepath.Join(dir, "legacy.cast")
			const archiveCount = 256
			start := time.Now().UnixNano() - archiveCount - 1
			for i := range archiveCount + 1 {
				name, output := path, "LATEST"
				if i < archiveCount {
					name = filepath.Join(dir, fmt.Sprintf("legacy.%d.cast", start+int64(i)))
					output = "old\r\n"
				}
				header := fmt.Sprintf("{\"version\":2,\"width\":80,\"height\":24,\"timestamp\":1,\"incarnation\":%d}\n", start+int64(i))
				data := append([]byte(header), castLine(time.Now(), "o", []byte(output))...)
				if err := os.WriteFile(name, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var original TerminalPosition
			if mode != "missing" {
				snapshot, err := repairColdSnapshot(path)
				if err != nil {
					t.Fatal(err)
				}
				original = snapshot.Position
				if mode == "v1" {
					checkpoint, decodeErr := decodeCheckpoint(checkpointPath(path))
					if decodeErr != nil {
						t.Fatal(decodeErr)
					}
					checkpoint.Version, checkpoint.Epoch, checkpoint.Sequence = 1, "", 0
					checkpoint.CastOutputBytes = 0
					if err = writeCheckpointFile(checkpointPath(path), checkpoint); err != nil {
						t.Fatal(err)
					}
				} else {
					f, openErr := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
					if openErr != nil {
						t.Fatal(openErr)
					}
					_, writeErr := f.Write(castLine(time.Now(), "o", []byte("-suffix")))
					closeErr := f.Close()
					if writeErr != nil || closeErr != nil {
						t.Fatalf("append suffix: %v, %v", writeErr, closeErr)
					}
				}
			}
			window, err := h.Replay("legacy")
			if err != nil {
				t.Fatal(err)
			}
			data, readErr := io.ReadAll(window.Reader)
			closeErr := window.Reader.Close()
			want := append(bytes.Repeat([]byte("old\r\n"), castRetainedSegments-1), []byte("LATEST")...)
			if mode == "stale" {
				want = append(want, "-suffix"...)
				if window.Position.Epoch != original.Epoch || window.Position.Sequence != original.Sequence+TerminalSequence(len("-suffix")) {
					t.Fatalf("stale repair lost absolute position: %+v, original %+v", window.Position, original)
				}
			}
			if readErr != nil || closeErr != nil || !bytes.Equal(data, want) || window.Bytes != len(want) || !window.TruncatedBefore || !window.Complete {
				t.Fatalf("retained replay = %q, %+v, %v, %v", data, window, readErr, closeErr)
			}
			checkpoint, err := decodeCheckpoint(checkpointPath(path))
			if err != nil {
				t.Fatal(err)
			}
			if checkpoint.CastOutputBytes != uint64(archiveCount*len("old\r\n")+len(want)-(castRetainedSegments-1)*len("old\r\n")) {
				t.Fatalf("absolute output boundary = %d", checkpoint.CastOutputBytes)
			}
			if !bytes.Contains(checkpoint.Data, []byte("LATEST")) || mode == "stale" && !bytes.Contains(checkpoint.Data, []byte("LATEST-suffix")) {
				t.Fatalf("latest compact screen lost: %q", checkpoint.Data)
			}
			segments, err := priorCastPaths(path)
			if err != nil || len(segments) != castRetainedSegments-1 {
				t.Fatalf("retained archives = %d, %v", len(segments), err)
			}
		})
	}
}
