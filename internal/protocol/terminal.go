package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
)

// TerminalRequest carries tab selection and terminal dimensions. Follow
// means the same as it does in AttachRequest: render at the session's size
// and impose none.
type TerminalRequest struct {
	Tab    string `json:"tab,omitempty"`
	Cols   uint   `json:"cols,omitempty"`
	Rows   uint   `json:"rows,omitempty"`
	Follow bool   `json:"follow,omitempty"`
	Framed bool   `json:"framed,omitempty"`
}

// TerminalImageParams carries the base64-encoded original image bytes.
// RunID empty targets the caller's environment terminal; when present it
// targets that run after the server checks steering authorization.
type TerminalImageParams struct {
	RunID   string `json:"run_id,omitempty"`
	Content string `json:"content"`
}

// TerminalImageResult is the absolute path to the uploaded image inside the
// target container.
type TerminalImageResult struct {
	Path string `json:"path"`
}

// TerminalResponse is the result of a terminal control operation.
type TerminalResponse struct {
	OK     bool   `json:"ok"`
	Tab    string `json:"tab,omitempty"`
	Cols   uint   `json:"cols,omitempty"`
	Rows   uint   `json:"rows,omitempty"`
	Framed bool   `json:"framed,omitempty"`
	// Replay is the number of bytes of scrollback replay that follow the ack before live output.
	Replay int    `json:"replay,omitempty"`
	Code   int    `json:"code,omitempty"`
	Error  string `json:"error,omitempty"`
}

// TerminalStatusResult is the wire form of domain.TerminalStatus.
type TerminalStatusResult struct {
	Running    bool     `json:"running"`
	Image      string   `json:"image,omitempty"`
	SavedImage string   `json:"saved_image,omitempty"`
	StartedAt  string   `json:"started_at,omitempty"`
	Tabs       []string `json:"tabs,omitempty"`
}

// EnvSaveResult is the result of saving a member's environment terminal.
type EnvSaveResult struct {
	Image string `json:"image"`
}

// TerminalReader decodes the ordered records requested by Framed. Output is
// streamed into p without allocating a frame-sized buffer; geometry is returned
// on its own, before the next output record.
type TerminalReader struct {
	Reader    io.Reader
	remaining uint32
}

func (r *TerminalReader) Read(p []byte) (n int, geometry [2]uint, err error) {
	if len(p) == 0 {
		return 0, geometry, nil
	}
	for r.remaining == 0 {
		var tag [1]byte
		if _, err = io.ReadFull(r.Reader, tag[:]); err != nil {
			return 0, geometry, err
		}
		switch tag[0] {
		case 'o':
			var length [4]byte
			if _, err = io.ReadFull(r.Reader, length[:]); err != nil {
				if err == io.EOF {
					err = io.ErrUnexpectedEOF
				}
				return 0, geometry, fmt.Errorf("terminal output length: %w", err)
			}
			r.remaining = binary.BigEndian.Uint32(length[:])
		case 'g':
			var size [8]byte
			if _, err = io.ReadFull(r.Reader, size[:]); err != nil {
				if err == io.EOF {
					err = io.ErrUnexpectedEOF
				}
				return 0, geometry, fmt.Errorf("terminal geometry: %w", err)
			}
			geometry = [2]uint{uint(binary.BigEndian.Uint32(size[:4])), uint(binary.BigEndian.Uint32(size[4:]))}
			if geometry[0] == 0 || geometry[1] == 0 {
				return 0, [2]uint{}, fmt.Errorf("terminal geometry is zero: %v", geometry)
			}
			return 0, geometry, nil
		default:
			return 0, geometry, fmt.Errorf("unknown terminal record %q", tag[0])
		}
	}
	if uint64(len(p)) > uint64(r.remaining) {
		p = p[:r.remaining]
	}
	n, err = io.ReadFull(r.Reader, p)
	r.remaining -= uint32(n)
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return n, geometry, err
}

// WriteTerminalOutput writes an output record, reporting only payload bytes.
func WriteTerminalOutput(w io.Writer, p []byte) (int, error) {
	if uint64(len(p)) > uint64(^uint32(0)) {
		return 0, fmt.Errorf("terminal output exceeds record length: %d", len(p))
	}
	var header [5]byte
	header[0] = 'o'
	binary.BigEndian.PutUint32(header[1:], uint32(len(p)))
	n, err := w.Write(header[:])
	if err != nil {
		return 0, err
	}
	if n != len(header) {
		return 0, io.ErrShortWrite
	}
	return w.Write(p)
}

// WriteTerminalGeometry writes a geometry record between output records.
func WriteTerminalGeometry(w io.Writer, cols, rows uint) error {
	var record [9]byte
	record[0] = 'g'
	binary.BigEndian.PutUint32(record[1:5], uint32(cols))
	binary.BigEndian.PutUint32(record[5:], uint32(rows))
	n, err := w.Write(record[:])
	if err == nil && n != len(record) {
		err = io.ErrShortWrite
	}
	return err
}
