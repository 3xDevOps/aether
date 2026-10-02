package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestDevelopmentFrameRejectsAdvertisedOversizeBeforeBody(t *testing.T) {
	for _, size := range [][2]uint32{{MaxDevFrameMetadataBytes + 1, 1}, {1, MaxDevFrameBytes + 1}, {0, 1}, {1, 0}} {
		var header [8]byte
		binary.BigEndian.PutUint32(header[:4], size[0])
		binary.BigEndian.PutUint32(header[4:], size[1])
		_, err := ReadDevBrowserFrame(bytes.NewReader(header[:]))
		if err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("oversized/empty header %v consumed body instead of refusal: %v", size, err)
		}
	}
}
func TestDevelopmentFrameReportsTruncatedBinaryBody(t *testing.T) {
	var encoded bytes.Buffer
	frame := DevBrowserFrame{Metadata: DevBrowserFrameMetadata{RunID: "run", SessionID: "session", PageID: "page", PageRevision: 3, ViewportID: "viewport", MIMEType: "image/jpeg", Width: 2, Height: 1}, Data: []byte{0xff, 0xd8, 0xff, 0xd9}}
	if err := WriteDevBrowserFrame(&encoded, frame); err != nil {
		t.Fatal(err)
	}
	truncated := encoded.Bytes()[:encoded.Len()-1]
	if _, err := ReadDevBrowserFrame(bytes.NewReader(truncated)); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated JPEG body: %v", err)
	}
	var denied bytes.Buffer
	sentinel := errors.New("view authority revoked")
	if err := WriteDevBrowserFrame(&denied, DevBrowserFrame{Err: sentinel}); !errors.Is(err, sentinel) || denied.Len() != 0 {
		t.Fatalf("error frame emitted image bytes: %v, %x", err, denied.Bytes())
	}
}
