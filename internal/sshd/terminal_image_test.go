package sshd

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/protocol"
)

var onePixelPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9c, 0x63, 0xf8, 0xcf, 0xc0, 0xf0,
	0x1f, 0x00, 0x05, 0x00, 0x01, 0xff, 0x89, 0x99,
	0x3d, 0x1d, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45,
	0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

func TestDecodeTerminalImageAcceptsExactDecodedLimit(t *testing.T) {
	t.Parallel()
	data := make([]byte, memberhome.MaxImageBytes)
	copy(data, onePixelPNG)
	encoded := base64.StdEncoding.EncodeToString(data)
	got, ext, err := decodeTerminalImage(encoded)
	if err != nil {
		t.Fatalf("decode exact-limit PNG: %v", err)
	}
	if ext != ".png" || len(got) != memberhome.MaxImageBytes || !bytes.Equal(got[:len(onePixelPNG)], onePixelPNG) {
		t.Fatalf("decoded image = extension %q, %d bytes", ext, len(got))
	}
}

func TestDecodeTerminalImageRejectsInvalidAndOversizedContent(t *testing.T) {
	t.Parallel()
	if _, _, err := decodeTerminalImage(base64.StdEncoding.EncodeToString([]byte("not an image"))); err == nil {
		t.Fatal("invalid image accepted")
	}
	oversize := make([]byte, memberhome.MaxImageBytes+1)
	if _, _, err := decodeTerminalImage(base64.StdEncoding.EncodeToString(oversize)); err == nil {
		t.Fatal("oversized image accepted")
	}
}

func TestDecodeTerminalImagePreservesValidationErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, encoded, want string
	}{
		{"required", "", "image content is required"},
		{"base64", "???", "image content is not valid base64:"},
		{"noncanonical base64", "/x==", "image content is not valid base64:"},
		{"empty decoded", "\n", "image content is empty"},
		{"invalid PNG", base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n")), "invalid PNG image:"},
		{"invalid JPEG", base64.StdEncoding.EncodeToString([]byte{0xff, 0xd8, 0xff}), "invalid JPEG image:"},
		{"invalid GIF", base64.StdEncoding.EncodeToString([]byte("GIF89a")), "invalid GIF image:"},
		{"invalid WebP", base64.StdEncoding.EncodeToString([]byte("RIFF0000WEBP")), "invalid WebP image:"},
		{"unsupported", base64.StdEncoding.EncodeToString([]byte("<svg/>")), "unsupported or invalid image format"},
		{"oversized", strings.Repeat("A", maxTerminalImageEncodedBytes+1), "image exceeds the 8 MiB limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, ext, err := decodeTerminalImage(tc.encoded)
			if err == nil || !strings.Contains(err.Error(), tc.want) || data != nil || ext != "" {
				t.Fatalf("decodeTerminalImage = (%d bytes, %q, %v), want %q", len(data), ext, err, tc.want)
			}
		})
	}
}

func TestTerminalImageTargetAuthorization(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	viewerKey, _ := addMember(t, e, "Viewer", "viewer", false)
	viewer := controlAs(t, e, viewerKey)
	err := viewer.Call("terminal.image", map[string]string{
		"run_id":  string(e.run.ID),
		"content": base64.StdEncoding.EncodeToString(onePixelPNG),
	}, nil)
	var pe *protocol.Error
	if err == nil || !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("viewer targeted terminal.image = %v, want CodeDenied", err)
	}
}
