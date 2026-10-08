package acphost

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestHugeCaptionRetainsRoomImageIdentity(t *testing.T) {
	image := Content{Type: "image", MimeType: "image/png", URI: "aether://room/message-1/0"}
	makeItem := func() Item {
		return Item{Kind: KindMessage, Message: &Message{
			Role: "user", MessageID: "local-message", Text: strings.Repeat("caption", MaxItemBytes),
			Attachments: []Content{image}, Complete: true,
		}}
	}
	assertImage := func(t *testing.T, b []byte, limit int) {
		t.Helper()
		if len(b) > limit {
			t.Fatalf("encoded %d bytes exceeds %d", len(b), limit)
		}
		var got Item
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if !got.Truncated || got.Message == nil || len(got.Message.Attachments) != 1 || got.Message.Attachments[0] != image {
			t.Fatalf("lost bounded image identity: %+v", got.Message)
		}
	}
	t.Run("log", func(t *testing.T) {
		it := makeItem()
		b, err := it.encode()
		if err != nil {
			t.Fatal(err)
		}
		assertImage(t, b, MaxItemBytes)
	})
	for _, limit := range []int{8192, 1024} {
		t.Run(fmt.Sprintf("wire-%d", limit), func(t *testing.T) {
			it := makeItem()
			b, truncated, err := it.Wire(limit)
			if err != nil || !truncated {
				t.Fatalf("Wire = truncated %v, %v", truncated, err)
			}
			assertImage(t, b, limit)
			if len(it.Message.Text) <= maxShrunkString {
				t.Fatal("Wire mutated original caption")
			}
		})
	}
}

func TestImageReferencesCannotInflateTruncatedFrames(t *testing.T) {
	invalid := []string{
		"https://example.com/image.png", "data:image/png;base64,private",
		"aether://room//0", "aether://room/message/-1", "aether://room/message/8",
		"aether://room/message/00", "aether://room/message/0?x=1", "aether://room/message/0#fragment",
		"aether://room/a/b/0", "aether://room/%zz/0", "aether://room/%00/0",
		"aether://room/" + strings.Repeat("x", MaxItemBytes) + "/0",
	}
	var attachments []Content
	for _, uri := range invalid {
		attachments = append(attachments, Content{Type: "image", MimeType: "image/png", URI: uri})
	}
	attachments = append(attachments, Content{Type: "image", MimeType: strings.Repeat("x", MaxItemBytes), URI: "aether://room/message/0"})
	for i := range 32 {
		attachments = append(attachments, Content{
			Type: "image", MimeType: "image/png", URI: fmt.Sprintf("aether://room/message/%d", i%8),
			Text: strings.Repeat("private", 1024), TerminalID: "private-terminal",
		})
	}
	it := Item{Kind: KindMessage, Message: &Message{Role: "user", MessageID: "local", Attachments: attachments}}
	for _, limit := range []int{8192, 1024} {
		b, truncated, err := it.Wire(limit)
		if err != nil || !truncated || len(b) > limit {
			t.Fatalf("Wire(%d): bytes=%d truncated=%v err=%v", limit, len(b), truncated, err)
		}
		var got Item
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if got.Message == nil || len(got.Message.Attachments) == 0 || len(got.Message.Attachments) > maxRoomImages {
			t.Fatalf("bounded references missing: %+v", got.Message)
		}
		for _, ref := range got.Message.Attachments {
			if !strings.HasPrefix(ref.URI, "aether://room/message/") || len(ref.URI) > maxRoomImageURI || ref.MimeType != "image/png" || ref.Text != "" || ref.TerminalID != "" {
				t.Fatalf("unsafe retained reference: %+v", ref)
			}
		}
		if strings.Contains(string(b), "private") || strings.Contains(string(b), "example.com") {
			t.Fatal("bulk or external image payload survived truncation")
		}
	}
}
