package ptyhost

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

const lastLineBytes = 16 << 10

// LastLine returns the last non-empty line of a run's agent terminal as a
// terminal renders it, or "" when nothing was recorded. A live session gets up
// to wait to end first: the PTY can still be draining after the container
// exited.
func (h *Host) LastLine(ctx context.Context, run domain.RunID, wait time.Duration) (string, error) {
	if err := validateRunID(run); err != nil {
		return "", fmt.Errorf("%w: %q", err, run)
	}
	key := RunSession(run)
	if s := h.lookup(key); s != nil {
		if err := s.awaitFinish(ctx, wait); err != nil {
			return "", err
		}
	}
	path := h.transcriptPath(key)
	data, err := readRecentCast(path, lastLineBytes)
	if err != nil {
		return "", err
	}
	header, err := newestCastHeader(path)
	if err != nil {
		return "", err
	}
	return renderLastLine(data, header.Width, header.Height)
}

func (s *session) awaitFinish(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-s.done:
	case <-timer.C:
		return s.flushLiveTranscript()
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	finished := s.finishDone
	s.mu.Unlock()
	if finished == nil {
		return nil
	}
	select {
	case <-finished:
	case <-timer.C:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func renderLastLine(data []byte, cols, rows uint) (string, error) {
	screen, err := newTerminalScreen(cols, rows)
	if err != nil {
		return "", err
	}
	defer screen.dispose()
	screen.write(data)
	buf := screen.term.Buffer()
	for y := buf.Lines.Length() - 1; y >= 0; y-- {
		line := buf.Lines.Get(y)
		if line == nil {
			continue
		}
		text := line.TranslateToString(true, 0, int(cols))
		for line.IsWrapped && y > 0 {
			y--
			line = buf.Lines.Get(y)
			if line == nil {
				break
			}
			text = line.TranslateToString(false, 0, int(cols)) + text
		}
		if text = strings.TrimSpace(text); text != "" {
			return text, nil
		}
	}
	return "", nil
}
