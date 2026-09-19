package protocol

import (
	"encoding/binary"
	"encoding/json"
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
// streamed into p without allocating a frame-sized buffer; geometry and
// control records are returned before the next output record.
type TerminalReader struct {
	Reader    io.Reader
	remaining uint32
	// Control is set when Read consumes an ordered control record. The next
	// Read clears it. Control records do not carry terminal output bytes.
	Control *DashAttachControl
}

const maxTerminalControlBytes = 1 << 20

func (r *TerminalReader) Read(p []byte) (n int, geometry [2]uint, err error) {
	r.Control = nil
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
		case 'c':
			var length [4]byte
			if _, err = io.ReadFull(r.Reader, length[:]); err != nil {
				if err == io.EOF {
					err = io.ErrUnexpectedEOF
				}
				return 0, geometry, fmt.Errorf("terminal control length: %w", err)
			}
			size := binary.BigEndian.Uint32(length[:])
			if size > maxTerminalControlBytes {
				return 0, geometry, fmt.Errorf("terminal control exceeds %d bytes", maxTerminalControlBytes)
			}
			payload := make([]byte, size)
			if _, err = io.ReadFull(r.Reader, payload); err != nil {
				if err == io.EOF {
					err = io.ErrUnexpectedEOF
				}
				return 0, geometry, fmt.Errorf("terminal control body: %w", err)
			}
			var control DashAttachControl
			if err = json.Unmarshal(payload, &control); err != nil {
				return 0, geometry, fmt.Errorf("terminal control JSON: %w", err)
			}
			r.Control = &control
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

// MarshalTerminalControl encodes a server-to-client terminal control record.
// Outbound results carry has_control explicitly, including false; inbound
// commands continue to use DashAttachControl's compact omitempty encoding.
func MarshalTerminalControl(control DashAttachControl) ([]byte, error) {
	type terminalControlFrame struct {
		DashAttachControl
		HasControl bool `json:"has_control"`
	}
	return json.Marshal(terminalControlFrame{
		DashAttachControl: control,
		HasControl:        control.HasControl,
	})
}

// WriteTerminalControl writes an ordered JSON control record.
func WriteTerminalControl(w io.Writer, control DashAttachControl) error {
	payload, err := MarshalTerminalControl(control)
	if err != nil {
		return err
	}
	if uint64(len(payload)) > uint64(maxTerminalControlBytes) {
		return fmt.Errorf("terminal control exceeds %d bytes", maxTerminalControlBytes)
	}
	var header [5]byte
	header[0] = 'c'
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	n, err := w.Write(header[:])
	if err != nil {
		return err
	}
	if n != len(header) {
		return io.ErrShortWrite
	}
	n, err = w.Write(payload)
	if err == nil && n != len(payload) {
		err = io.ErrShortWrite
	}
	return err
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
