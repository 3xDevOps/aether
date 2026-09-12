package localops

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"unicode/utf16"
)

// Shell Link (.lnk) serialization, per [MS-SHLLINK].
//
// The Start Menu entry used to be written by a PowerShell one-liner driving
// the WScript.Shell COM object. That spawned a PowerShell child during the
// install and left `New-Object -ComObject WScript.Shell` and `CreateShortcut`
// in the shipped binary as literal strings - the exact shape antivirus
// signatures look for in persistence malware, whether or not the code ever
// runs. A few hundred bytes written here keep both out of the client.
//
// Nothing but a target and a working directory is modelled: that is all a
// Start Menu entry needs, and every field the format allows past that is one
// more thing to get wrong.

const (
	shellLinkHeaderSize = 0x4C

	// LinkFlags: the link carries a LinkInfo block and a working directory,
	// and its StringData is UTF-16.
	flagHasLinkInfo   = 0x00000002
	flagHasWorkingDir = 0x00000010
	flagIsUnicode     = 0x00000080

	fileAttributeNormal = 0x00000080
	showCommandNormal   = 0x00000001

	// LinkInfo with the Unicode path fields present. %LOCALAPPDATA% holds a
	// user name, so a shortcut that could only express a code page would
	// break for anyone whose name is not ASCII.
	linkInfoHeaderSizeUnicode    = 0x24
	flagVolumeIDAndLocalBasePath = 0x00000001

	volumeIDSize      = 17 // 16-byte header plus an empty label
	driveTypeFixed    = 0x00000003
	volumeLabelOffset = 0x10
)

// linkCLSID is {00021401-0000-0000-C000-000000000046} in the mixed-endian
// layout a GUID takes on the wire.
var linkCLSID = [16]byte{
	0x01, 0x14, 0x02, 0x00,
	0x00, 0x00,
	0x00, 0x00,
	0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46,
}

// writeShellLink creates a Start Menu shortcut at path pointing at target.
func writeShellLink(path, target, workingDir string) error {
	if err := os.WriteFile(path, shellLink(target, workingDir), 0o644); err != nil {
		return fmt.Errorf("write shell link %s: %w", path, err)
	}
	return nil
}

// shellLink marshals a .lnk pointing at target and starting in workingDir.
func shellLink(target, workingDir string) []byte {
	var b bytes.Buffer

	put32 := func(v uint32) { _ = binary.Write(&b, binary.LittleEndian, v) }
	put16 := func(v uint16) { _ = binary.Write(&b, binary.LittleEndian, v) }

	put32(shellLinkHeaderSize)
	b.Write(linkCLSID[:])
	put32(flagHasLinkInfo | flagHasWorkingDir | flagIsUnicode)
	put32(fileAttributeNormal)
	// CreationTime, AccessTime and WriteTime, two words each. They describe
	// the target rather than the link, and resolving the link does not read
	// them, so they are left zeroed.
	for range 6 {
		put32(0)
	}
	put32(0) // FileSize
	put32(0) // IconIndex - the target's own icon
	put32(showCommandNormal)
	put16(0) // HotKey
	put16(0) // Reserved1
	put32(0) // Reserved2
	put32(0) // Reserved3

	b.Write(linkInfo(target))

	// StringData: the working directory, as a counted run of UTF-16 code
	// units with no terminator. The count is a uint16 and Windows caps a
	// path at 32767 units even with long paths enabled, so it cannot wrap.
	dir := utf16.Encode([]rune(workingDir))
	put16(uint16(len(dir)))
	for _, u := range dir {
		put16(u)
	}

	put32(0) // ExtraData terminal block
	return b.Bytes()
}

// linkInfo builds the LinkInfo block locating target on a local volume. It
// carries the path twice, once in a code page and once in UTF-16: readers
// that understand the Unicode fields prefer them, and the code-page copy is
// what everything older falls back to.
func linkInfo(target string) []byte {
	ansi := append(codePagePath(target), 0)
	unicodePath := utf16.Encode([]rune(target))

	localBasePathOffset := uint32(linkInfoHeaderSizeUnicode + volumeIDSize)
	commonPathSuffixOffset := localBasePathOffset + uint32(len(ansi))
	// The empty common suffix is one terminator in each encoding.
	localBasePathOffsetUnicode := commonPathSuffixOffset + 1
	commonPathSuffixOffsetUnicode := localBasePathOffsetUnicode + uint32(len(unicodePath)+1)*2
	size := commonPathSuffixOffsetUnicode + 2

	var b bytes.Buffer
	put32 := func(v uint32) { _ = binary.Write(&b, binary.LittleEndian, v) }
	put16 := func(v uint16) { _ = binary.Write(&b, binary.LittleEndian, v) }

	put32(size)
	put32(linkInfoHeaderSizeUnicode)
	put32(flagVolumeIDAndLocalBasePath)
	put32(linkInfoHeaderSizeUnicode) // VolumeIDOffset
	put32(localBasePathOffset)
	put32(0) // CommonNetworkRelativeLinkOffset: the target is local
	put32(commonPathSuffixOffset)
	put32(localBasePathOffsetUnicode)
	put32(commonPathSuffixOffsetUnicode)

	// VolumeID. The drive serial and label are cosmetic - the shell resolves
	// through the path - so the label is left empty.
	put32(volumeIDSize)
	put32(driveTypeFixed)
	put32(0) // DriveSerialNumber
	put32(volumeLabelOffset)
	b.WriteByte(0)

	b.Write(ansi)
	b.WriteByte(0) // CommonPathSuffix
	for _, u := range unicodePath {
		put16(u)
	}
	put16(0) // LocalBasePathUnicode terminator
	put16(0) // CommonPathSuffixUnicode

	return b.Bytes()
}

// codePagePath renders target for the code-page copy of the path. A byte a
// code page cannot be trusted to carry becomes '?', which is not a legal path
// character: a reader that ignores the UTF-16 copy alongside it fails to
// resolve the shortcut rather than opening some other file.
func codePagePath(target string) []byte {
	out := make([]byte, 0, len(target))
	for _, r := range target {
		if r < 0x80 {
			out = append(out, byte(r))
			continue
		}
		out = append(out, '?')
	}
	return out
}
