package localops

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

// decodeUTF16 reads n UTF-16 code units at off.
func decodeUTF16(t *testing.T, b []byte, off, n int) string {
	t.Helper()
	if off+n*2 > len(b) {
		t.Fatalf("UTF-16 run at %d (%d units) runs past the %d-byte link", off, n, len(b))
	}
	u := make([]uint16, n)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[off+i*2:])
	}
	return string(utf16.Decode(u))
}

func cstring(b []byte, off int) string {
	for i := off; i < len(b); i++ {
		if b[i] == 0 {
			return string(b[off:i])
		}
	}
	return ""
}

func TestShellLinkHeader(t *testing.T) {
	const target = `C:\Users\dev\AppData\Local\Programs\Aether\Aether.exe`
	link := shellLink(target, `C:\Users\dev\AppData\Local\Programs\Aether`)

	if got := binary.LittleEndian.Uint32(link[0:]); got != shellLinkHeaderSize {
		t.Errorf("HeaderSize = %#x, want %#x", got, shellLinkHeaderSize)
	}
	// A reader identifies a .lnk by this CLSID; a wrong byte makes the file
	// unopenable rather than merely wrong.
	if got := link[4:20]; string(got) != string(linkCLSID[:]) {
		t.Errorf("LinkCLSID = % x, want % x", got, linkCLSID)
	}
	flags := binary.LittleEndian.Uint32(link[20:])
	for _, f := range []struct {
		name string
		bit  uint32
	}{
		{"HasLinkInfo", flagHasLinkInfo},
		{"HasWorkingDir", flagHasWorkingDir},
		{"IsUnicode", flagIsUnicode},
	} {
		if flags&f.bit == 0 {
			t.Errorf("LinkFlags %#x does not set %s (%#x)", flags, f.name, f.bit)
		}
	}
	// HasLinkTargetIDList would promise an IDList this writer never emits.
	if flags&0x00000001 != 0 {
		t.Errorf("LinkFlags %#x claims a LinkTargetIDList that is not written", flags)
	}
	if got := binary.LittleEndian.Uint32(link[60:]); got != showCommandNormal {
		t.Errorf("ShowCommand = %d, want %d", got, showCommandNormal)
	}
}

func TestShellLinkResolvesTargetAndWorkingDir(t *testing.T) {
	const (
		target  = `C:\Users\dev\AppData\Local\Programs\Aether\Aether.exe`
		workdir = `C:\Users\dev\AppData\Local\Programs\Aether`
	)
	link := shellLink(target, workdir)
	info := shellLinkHeaderSize

	size := binary.LittleEndian.Uint32(link[info:])
	if int(size) != len(link)-info-2-len(utf16.Encode([]rune(workdir)))*2-4 {
		t.Errorf("LinkInfoSize %d does not account for the bytes that follow it", size)
	}
	if got := binary.LittleEndian.Uint32(link[info+4:]); got != linkInfoHeaderSizeUnicode {
		t.Errorf("LinkInfoHeaderSize = %#x, want %#x", got, linkInfoHeaderSizeUnicode)
	}

	basePath := binary.LittleEndian.Uint32(link[info+16:])
	baseUnicode := binary.LittleEndian.Uint32(link[info+28:])
	if got := cstring(link, info+int(basePath)); got != target {
		t.Errorf("LocalBasePath = %q, want %q", got, target)
	}
	if got := decodeUTF16(t, link, info+int(baseUnicode), len([]rune(target))); got != target {
		t.Errorf("LocalBasePathUnicode = %q, want %q", got, target)
	}

	// StringData follows LinkInfo: a count of UTF-16 units, then the run.
	at := info + int(size)
	n := int(binary.LittleEndian.Uint16(link[at:]))
	if got := decodeUTF16(t, link, at+2, n); got != workdir {
		t.Errorf("working directory = %q, want %q", got, workdir)
	}
	if tail := at + 2 + n*2; len(link) != tail+4 {
		t.Errorf("link is %d bytes, want %d (ExtraData terminator only)", len(link), tail+4)
	}
	if got := binary.LittleEndian.Uint32(link[len(link)-4:]); got != 0 {
		t.Errorf("ExtraData terminator = %#x, want 0", got)
	}
}

// A user name outside ASCII must survive in the UTF-16 copy of the path; the
// code-page copy beside it is allowed to be lossy but must stay the same
// length, or every offset after it moves.
func TestShellLinkCarriesNonASCIIPath(t *testing.T) {
	const target = `C:\Users\José\Programs\Aether\Aether.exe`
	link := shellLink(target, `C:\Users\José\Programs\Aether`)
	info := shellLinkHeaderSize

	basePath := int(binary.LittleEndian.Uint32(link[info+16:]))
	baseUnicode := int(binary.LittleEndian.Uint32(link[info+28:]))

	if got := decodeUTF16(t, link, info+baseUnicode, len([]rune(target))); got != target {
		t.Errorf("LocalBasePathUnicode = %q, want %q", got, target)
	}
	ansi := cstring(link, info+basePath)
	if len(ansi) != len([]rune(target)) {
		t.Errorf("code-page path is %d bytes for a %d-rune target; offsets after it would shift", len(ansi), len([]rune(target)))
	}
	if ansi == target {
		t.Errorf("code-page path %q cannot represent the target verbatim", ansi)
	}
}
