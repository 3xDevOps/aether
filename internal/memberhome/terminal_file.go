package memberhome

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/domain"
)

const (
	terminalFileDir     = ".aether/terminal-files"
	maxFileNameBytes    = 128
	maxFileNameExtBytes = 16
)

var fileDirPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// SafeFileName turns an uploaded file's original name into one path
// component. It is idempotent, which is how ValidFileName recognises its
// output.
func SafeFileName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || !unicode.IsPrint(r) {
			return '_'
		}
		return r
	}, name)
	if len(name) > maxFileNameBytes {
		ext := path.Ext(name)
		if len(ext) > maxFileNameExtBytes {
			ext = ""
		}
		cut := maxFileNameBytes - len(ext)
		for cut > 0 && !utf8.RuneStart(name[cut]) {
			cut--
		}
		name = name[:cut] + ext
	}
	name = strings.TrimRight(strings.TrimLeft(name, ". "), " ")
	if name == "" {
		return "file"
	}
	return name
}

// ValidFileName accepts only a server-generated "<32 hex>/<safe name>"
// reference below the terminal file directory.
func ValidFileName(name string) bool {
	dir, base, ok := strings.Cut(name, "/")
	return ok && fileDirPattern.MatchString(dir) && SafeFileName(base) == base
}

// SaveFile writes one uploaded file into a fresh generated directory of the
// member's persistent home, keeping its sanitised original name, and returns
// the home-relative path.
func (m *Manager) SaveFile(member domain.MemberID, name string, data []byte) (string, error) {
	if len(data) > ConfigMaxFileBytes {
		return "", fmt.Errorf("memberhome: file exceeds %d bytes", ConfigMaxFileBytes)
	}
	root, err := m.openHome(member)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	name = SafeFileName(name)
	for range 10 {
		var suffix [terminalImageNameBytes]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return "", fmt.Errorf("memberhome: generate file directory for %q: %w", member, err)
		}
		dir := terminalFileDir + "/" + hex.EncodeToString(suffix[:])
		if err := ensureDir(root, dir); err != nil {
			return "", fmt.Errorf("memberhome: prepare file directory for %q: %w", member, err)
		}
		if err := writeNew(root, dir+"/"+name, data, 0o600); err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			_ = root.RemoveAll(dir)
			return "", fmt.Errorf("memberhome: write file for %q: %w", member, err)
		}
		return dir + "/" + name, nil
	}
	return "", fmt.Errorf("memberhome: generate unique file directory for %q", member)
}

// StatFile reports the size of a generated file in an existing persistent
// home and the MIME type sniffed from its first bytes. All components are
// opened without following links.
func (m *Manager) StatFile(member domain.MemberID, name string) (int64, string, error) {
	if !ValidFileName(name) {
		return 0, "", fmt.Errorf("memberhome: invalid file name %q", name)
	}
	root, err := m.openExistingHome(member)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = root.Close() }()
	f, info, err := openRegular(root, terminalFileDir+"/"+name)
	if err != nil {
		return 0, "", fmt.Errorf("memberhome: open file for %q: %w", member, err)
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, "", fmt.Errorf("memberhome: read file for %q: %w", member, err)
	}
	return info.Size(), http.DetectContentType(head[:n]), nil
}
