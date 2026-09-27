package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"time"
)

const (
	SubsystemDevBrowser      = "aether-dev-browser"
	SubsystemDevArtifact     = "aether-dev-artifact"
	MaxDevFrameMetadataBytes = 16 << 10
	MaxDevFrameBytes         = 2 << 20
)

type DevBrowserStreamRequest struct{ DevBrowserPageTarget }
type DevArtifactDownloadRequest struct {
	DevArtifactGetParams
	EvidencePacketID string `json:"evidence_packet_id,omitempty"`
}
type DevStreamResponse struct {
	OK       bool         `json:"ok"`
	Error    string       `json:"error,omitempty"`
	Code     int          `json:"code,omitempty"`
	Artifact *DevArtifact `json:"artifact,omitempty"`
}
type DevBrowserFrameMetadata struct {
	RunID           string    `json:"run_id"`
	SessionID       string    `json:"session_id"`
	PageID          string    `json:"page_id"`
	PageRevision    uint64    `json:"page_revision"`
	ViewportID      string    `json:"viewport_id"`
	Width           int       `json:"width"`
	Height          int       `json:"height"`
	Timestamp       time.Time `json:"timestamp"`
	MIMEType        string    `json:"mime_type"`
	Sequence        uint64    `json:"sequence"`
	OffsetTop       float64   `json:"offset_top,omitempty"`
	PageScaleFactor float64   `json:"page_scale_factor,omitempty"`
	ScrollX         float64   `json:"scroll_x,omitempty"`
	ScrollY         float64   `json:"scroll_y,omitempty"`
}
type DevBrowserFrame struct {
	Metadata DevBrowserFrameMetadata
	Data     []byte
	Err      error `json:"-"`
}

// WriteDevBrowserFrame keeps pixels out of the bounded control protocol.
func WriteDevBrowserFrame(w io.Writer, frame DevBrowserFrame) error {
	if frame.Err != nil {
		return frame.Err
	}
	meta, err := json.Marshal(frame.Metadata)
	if err != nil {
		return err
	}
	if len(meta) > MaxDevFrameMetadataBytes || len(frame.Data) == 0 || len(frame.Data) > MaxDevFrameBytes {
		return errors.New("invalid browser frame size")
	}
	var header [8]byte
	binary.BigEndian.PutUint32(header[:4], uint32(len(meta)))
	binary.BigEndian.PutUint32(header[4:], uint32(len(frame.Data)))
	for _, data := range [][]byte{header[:], meta, frame.Data} {
		for len(data) > 0 {
			n, err := w.Write(data)
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrShortWrite
			}
			data = data[n:]
		}
	}
	return nil
}
func ReadDevBrowserFrame(r io.Reader) (DevBrowserFrame, error) {
	var frame DevBrowserFrame
	var header [8]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return frame, err
	}
	m, n := binary.BigEndian.Uint32(header[:4]), binary.BigEndian.Uint32(header[4:])
	if m == 0 || m > MaxDevFrameMetadataBytes || n == 0 || n > MaxDevFrameBytes {
		return frame, errors.New("invalid browser frame size")
	}
	meta := make([]byte, m)
	if _, err := io.ReadFull(r, meta); err != nil {
		return frame, err
	}
	if err := json.Unmarshal(meta, &frame.Metadata); err != nil {
		return frame, err
	}
	frame.Data = make([]byte, n)
	_, err := io.ReadFull(r, frame.Data)
	return frame, err
}
