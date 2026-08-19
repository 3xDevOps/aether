package adapter

// normState tracks where the normalizer is inside a terminal escape
// sequence between Feed calls, so sequences split across chunk boundaries
// are still stripped.
type normState int

const (
	statePlain normState = iota
	// stateEsc: after ESC - intermediates 0x20-0x2F, then one final byte.
	stateEsc
	// stateCSI: inside ESC [ ... until a final byte 0x40-0x7E.
	stateCSI
	// stateOSC: inside ESC ] ... until BEL or ESC \ (ST).
	stateOSC
	// stateOSCEsc: saw ESC inside an OSC string (potential ST terminator).
	stateOSCEsc
)

// maxLineBytes caps a pending line (mirrors protocol.MaxLineBytes). A
// harness emitting an endless unterminated line must never grow server
// memory: past the cap the line's remaining bytes are discarded until the
// next terminator and the truncated line is dropped as opaque output.
const maxLineBytes = 1 << 20

// LineNormalizer incrementally converts raw PTY output into clean text
// lines: escape sequences (CSI, OSC, and other ESC sequences) are stripped,
// CR and LF both terminate a line (so CRLF and bare-CR redraws never leak a
// carriage return into a line), remaining C0 controls and DEL are dropped,
// and partial lines are buffered until their terminator arrives. Empty
// lines are suppressed, and lines longer than maxLineBytes are discarded
// wholesale (opaque output, per the degradation contract). The zero value
// is ready to use.
type LineNormalizer struct {
	state    normState
	buf      []byte
	overflow bool // pending line exceeded maxLineBytes: discard until terminator
}

// Feed consumes the next chunk of raw PTY bytes and returns the complete
// lines it finished, in order. Chunk boundaries are arbitrary: lines and
// escape sequences may span any number of Feed calls.
func (n *LineNormalizer) Feed(p []byte) []string {
	var lines []string
	for _, b := range p {
		switch n.state {
		case stateEsc:
			switch {
			case b == '[':
				n.state = stateCSI
			case b == ']':
				n.state = stateOSC
			case b >= 0x20 && b <= 0x2f:
				// Intermediate byte; the final byte is still ahead.
			default:
				n.state = statePlain
			}
		case stateCSI:
			if b >= 0x40 && b <= 0x7e {
				n.state = statePlain
			}
		case stateOSC:
			switch b {
			case 0x07: // BEL terminator
				n.state = statePlain
			case 0x1b:
				n.state = stateOSCEsc
			}
		case stateOSCEsc:
			switch b {
			case '\\': // ESC \ (ST) terminator
				n.state = statePlain
			case 0x1b:
				// Still a potential ST start.
			default:
				n.state = stateOSC
			}
		default: // statePlain
			switch {
			case b == 0x1b:
				n.state = stateEsc
			case b == '\n' || b == '\r':
				if len(n.buf) > 0 && !n.overflow {
					lines = append(lines, string(n.buf))
				}
				n.buf = n.buf[:0]
				n.overflow = false
			case b == '\t' || (b >= 0x20 && b != 0x7f):
				if n.overflow {
					break
				}
				if len(n.buf) >= maxLineBytes {
					n.overflow = true
					n.buf = n.buf[:0]
					break
				}
				n.buf = append(n.buf, b)
			default:
				// Remaining C0 controls and DEL are dropped.
			}
		}
	}
	return lines
}

// Flush returns the buffered trailing partial line, if any. Call it when
// the output stream ends: the last line of a headless run may lack a
// terminator.
func (n *LineNormalizer) Flush() (string, bool) {
	if len(n.buf) == 0 || n.overflow {
		n.buf = n.buf[:0]
		n.overflow = false
		return "", false
	}
	line := string(n.buf)
	n.buf = n.buf[:0]
	return line, true
}
