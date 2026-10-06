package ptyhost

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestLastLineRendersTheFinalOutputOfAnEndedSession(t *testing.T) {
	h, _ := newTestHost(t)
	run := domain.RunID("run-last-line")
	att := newFakeAtt()
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	long := strings.Repeat("x", 100)
	att.writeOutput(t, "working\r\nprogress 10%\rprogress 99%\r\n")
	att.writeOutput(t, "\x1b[31mError: "+long+"\x1b[0m\r\n\r\n   \r\n")
	_ = att.outW.Close()

	got, err := h.LastLine(context.Background(), run, 5*time.Second)
	if err != nil {
		t.Fatalf("LastLine: %v", err)
	}
	if want := "Error: " + long; got != want {
		t.Fatalf("LastLine = %q, want %q", got, want)
	}
}

func TestLastLineWithoutATranscript(t *testing.T) {
	h, _ := newTestHost(t)
	if _, err := h.LastLine(context.Background(), "run-never-started", time.Second); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LastLine for a run that recorded nothing = %v, want os.ErrNotExist", err)
	}
}
