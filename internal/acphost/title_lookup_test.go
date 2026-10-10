package acphost

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestTitleLookupFollowsTheSessionList(t *testing.T) {
	another := map[string]any{"sessionId": "another-session", "cwd": "/workspace", "title": "Another run"}

	t.Run("to the page that lists the session", func(t *testing.T) {
		m := newMockAgent(t, loadFixture(t, "claude"))
		m.onList = func(cursor string) any {
			if cursor != "page-2" {
				return map[string]any{"sessions": []any{another}, "nextCursor": "page-2"}
			}
			return map[string]any{"sessions": []any{map[string]any{"sessionId": m.sessionID(), "cwd": "/workspace", "title": "Later page title"}}}
		}
		titles := make(chan string, 1)
		s, rec := startMock(t, m, Config{TitleLookup: 10 * time.Millisecond, OnTitle: func(title string) { titles <- title }})
		if _, err := s.Prompt(context.Background(), textPrompt("say pong"), false, nil); err != nil {
			t.Fatal(err)
		}
		rec.waitIdle(t)
		select {
		case title := <-titles:
			if title != "Later page title" {
				t.Fatalf("title = %q, want the one on the second page", title)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the lookup stopped at the first page")
		}
	})

	t.Run("for a bounded number of pages", func(t *testing.T) {
		m := newMockAgent(t, loadFixture(t, "claude"))
		var requests atomic.Int32
		m.onList = func(string) any {
			return map[string]any{"sessions": []any{another}, "nextCursor": fmt.Sprint("page-", requests.Add(1)+1)}
		}
		s, rec := startMock(t, m, Config{TitleLookup: 10 * time.Millisecond})
		if _, err := s.Prompt(context.Background(), textPrompt("say pong"), false, nil); err != nil {
			t.Fatal(err)
		}
		rec.waitIdle(t)
		for deadline := time.Now().Add(5 * time.Second); requests.Load() < titleLookupPages; {
			if time.Now().After(deadline) {
				t.Fatalf("%d list requests, want %d", requests.Load(), titleLookupPages)
			}
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		if got := requests.Load(); got != titleLookupPages {
			t.Fatalf("%d list requests, want the lookup to stop at %d", got, titleLookupPages)
		}
	})
}
