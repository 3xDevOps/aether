package sshd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// ConfigBackend serves only the authenticated member's persistent harness
// roots. Implementations must keep file bytes in the member home and must not
// accept a member selector from request parameters.
type ConfigBackend interface {
	Roots(ctx context.Context, member domain.MemberID) ([]protocol.ConfigRoot, error)
	Tree(ctx context.Context, member domain.MemberID, harnessName, path string) ([]protocol.ConfigTreeEntry, error)
	Read(ctx context.Context, member domain.MemberID, harnessName, path string) (protocol.ConfigFileReadResult, error)
	Write(ctx context.Context, member domain.MemberID, harnessName, path, content, revision string) (protocol.ConfigFileReadResult, error)
	Import(ctx context.Context, member domain.MemberID, harnessName string, files []memberhome.ConfigFile) (protocol.ConfigImportResult, error)
}

// HomeConfigBackend is the persistent-home implementation. Harness definitions
// are resolved from the caller's own member row; administrators do not get a
// way to select another member's roots.
type HomeConfigBackend struct {
	homes *memberhome.Manager
	store store.Store
}

func NewConfigBackend(homes *memberhome.Manager, st store.Store) *HomeConfigBackend {
	return &HomeConfigBackend{homes: homes, store: st}
}

func init() {
	registerGuarded(protocol.MethodConfigRoots, permissions.Launch, nil, (*Server).configRoots)
	registerGuarded(protocol.MethodConfigTree, permissions.Launch, nil, (*Server).configTree)
	registerGuarded(protocol.MethodConfigRead, permissions.Launch, nil, (*Server).configRead)
	registerGuarded(protocol.MethodConfigWrite, permissions.Launch, nil, (*Server).configWrite)
	registerGuarded(protocol.MethodConfigImport, permissions.Launch, nil, (*Server).configImport)
}

func (b *HomeConfigBackend) profile(ctx context.Context, member domain.MemberID, name string) (harness.Profile, error) {
	if p, ok := harness.Lookup(name); ok && p.LocalRoot != "" {
		return p, nil
	}
	if b.store == nil {
		return harness.Profile{}, fmt.Errorf("%w: harness is unavailable", memberhome.ErrConfigDenied)
	}
	row, err := b.store.GetHarnessDefinition(ctx, member, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return harness.Profile{}, fmt.Errorf("%w: unknown harness", memberhome.ErrConfigDenied)
		}
		return harness.Profile{}, err
	}
	var definition harness.Definition
	if err := json.Unmarshal(row.Definition, &definition); err != nil {
		return harness.Profile{}, fmt.Errorf("%w: invalid harness", memberhome.ErrConfigDenied)
	}
	if err := definition.Validate(); err != nil {
		return harness.Profile{}, fmt.Errorf("%w: invalid harness", memberhome.ErrConfigDenied)
	}
	p := definition.Profile()
	if p.LocalRoot == "" {
		return harness.Profile{}, fmt.Errorf("%w: harness has no config root", memberhome.ErrConfigDenied)
	}
	p.LocalRoot = harness.HomeRelative(p.LocalRoot)
	return p, nil
}

func displayRoot(localRoot string) string {
	return "~/" + strings.TrimPrefix(harness.HomeRelative(localRoot), "./")
}

func (b *HomeConfigBackend) Roots(ctx context.Context, member domain.MemberID) ([]protocol.ConfigRoot, error) {
	seen := make(map[string]struct{})
	out := make([]protocol.ConfigRoot, 0)
	for _, p := range harness.Profiles() {
		if p.LocalRoot == "" {
			continue
		}
		seen[p.Name] = struct{}{}
		out = append(out, protocol.ConfigRoot{Harness: p.Name, Path: displayRoot(p.LocalRoot)})
	}
	if b.store != nil {
		defs, err := b.store.ListHarnessDefinitions(ctx, member)
		if err != nil {
			return nil, err
		}
		for _, row := range defs {
			if _, exists := seen[row.Name]; exists {
				continue
			}
			var definition harness.Definition
			if err := json.Unmarshal(row.Definition, &definition); err != nil {
				continue
			}
			p := definition.Profile()
			if p.LocalRoot == "" {
				continue
			}
			seen[row.Name] = struct{}{}
			out = append(out, protocol.ConfigRoot{Harness: row.Name, Path: displayRoot(p.LocalRoot)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Harness < out[j].Harness })
	return out, nil
}

func (b *HomeConfigBackend) Tree(ctx context.Context, member domain.MemberID, name, reqPath string) ([]protocol.ConfigTreeEntry, error) {
	if b.homes == nil {
		return nil, memberhome.ErrConfigNotFound
	}
	p, err := b.profile(ctx, member, name)
	if err != nil {
		return nil, err
	}
	entries, err := b.homes.ConfigTree(ctx, member, name, p.LocalRoot, reqPath, p.DenyNames)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.ConfigTreeEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, protocol.ConfigTreeEntry{Name: entry.Name, Kind: entry.Kind, Size: entry.Size})
	}
	return out, nil
}

func (b *HomeConfigBackend) Read(ctx context.Context, member domain.MemberID, name, reqPath string) (protocol.ConfigFileReadResult, error) {
	if b.homes == nil {
		return protocol.ConfigFileReadResult{}, memberhome.ErrConfigNotFound
	}
	p, err := b.profile(ctx, member, name)
	if err != nil {
		return protocol.ConfigFileReadResult{}, err
	}
	read, err := b.homes.ConfigRead(ctx, member, name, p.LocalRoot, reqPath, p.DenyNames)
	if err != nil {
		return protocol.ConfigFileReadResult{}, err
	}
	return configReadResult(read), nil
}

func (b *HomeConfigBackend) Write(ctx context.Context, member domain.MemberID, name, reqPath, content, revision string) (protocol.ConfigFileReadResult, error) {
	if b.homes == nil {
		return protocol.ConfigFileReadResult{}, memberhome.ErrConfigNotFound
	}
	p, err := b.profile(ctx, member, name)
	if err != nil {
		return protocol.ConfigFileReadResult{}, err
	}
	read, err := b.homes.ConfigWrite(ctx, member, name, p.LocalRoot, reqPath, []byte(content), revision, p.DenyNames)
	if err != nil {
		return protocol.ConfigFileReadResult{}, err
	}
	return configReadResult(read), nil
}

func (b *HomeConfigBackend) Import(ctx context.Context, member domain.MemberID, name string, files []memberhome.ConfigFile) (protocol.ConfigImportResult, error) {
	if b.homes == nil {
		return protocol.ConfigImportResult{}, memberhome.ErrConfigNotFound
	}
	p, err := b.profile(ctx, member, name)
	if err != nil {
		return protocol.ConfigImportResult{}, err
	}
	result, err := b.homes.ConfigImport(ctx, member, name, p.LocalRoot, files, p.DenyNames)
	if err != nil {
		return protocol.ConfigImportResult{}, err
	}
	out := protocol.ConfigImportResult{Harness: name, Files: result.Files, Bytes: result.Bytes, Excluded: make([]protocol.ConfigExcluded, 0, len(result.Excluded))}
	for _, excluded := range result.Excluded {
		out.Excluded = append(out.Excluded, protocol.ConfigExcluded{Path: excluded.Path, Reason: excluded.Reason, Detail: excluded.Detail})
	}
	return out, nil
}

func configReadResult(read memberhome.ConfigRead) protocol.ConfigFileReadResult {
	return protocol.ConfigFileReadResult{
		Content:   string(read.Content),
		Truncated: read.Truncated,
		Binary:    read.Binary,
		Size:      read.Size,
		Revision:  read.Revision,
		Writable:  read.Writable,
	}
}

func (s *Server) configRoots(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	if s.cfg.Config == nil {
		return nil, configUnavailable()
	}
	roots, err := s.cfg.Config.Roots(ctx, member)
	if err != nil {
		return nil, configError(err)
	}
	if roots == nil {
		roots = []protocol.ConfigRoot{}
	}
	return protocol.ConfigRootsResult{Roots: roots}, nil
}

func (s *Server) configTree(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.ConfigTreeParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.Harness == "" {
		return nil, invalidParams("harness is required")
	}
	if s.cfg.Config == nil {
		return nil, configUnavailable()
	}
	entries, err := s.cfg.Config.Tree(ctx, member, p.Harness, p.Path)
	if err != nil {
		return nil, configError(err)
	}
	if entries == nil {
		entries = []protocol.ConfigTreeEntry{}
	}
	return protocol.ConfigTreeResult{Entries: entries}, nil
}

func (s *Server) configRead(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.ConfigReadParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.Harness == "" || p.Path == "" {
		return nil, invalidParams("harness and path are required")
	}
	if s.cfg.Config == nil {
		return nil, configUnavailable()
	}
	read, err := s.cfg.Config.Read(ctx, member, p.Harness, p.Path)
	if err != nil {
		return nil, configError(err)
	}
	return read, nil
}

func (s *Server) configWrite(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.ConfigWriteParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.Harness == "" || p.Path == "" {
		return nil, invalidParams("harness and path are required")
	}
	if s.cfg.Config == nil {
		return nil, configUnavailable()
	}
	read, err := s.cfg.Config.Write(ctx, member, p.Harness, p.Path, p.Content, p.Revision)
	if err != nil {
		return nil, configError(err)
	}
	return read, nil
}

func (s *Server) configImport(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	var raw struct {
		Harness string          `json:"harness"`
		Files   json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(params, &raw); err != nil {
		return nil, invalidParams("invalid params: " + err.Error())
	}
	if raw.Harness == "" {
		return nil, invalidParams("harness is required")
	}
	if s.cfg.Config == nil {
		return nil, configUnavailable()
	}
	files, err := decodeConfigImportFiles(raw.Files)
	if err != nil {
		return nil, invalidParams(err.Error())
	}
	result, err := s.cfg.Config.Import(ctx, member, raw.Harness, files)
	if err != nil {
		return nil, configError(err)
	}
	return result, nil
}

func decodeConfigImportFiles(raw json.RawMessage) ([]memberhome.ConfigFile, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return []memberhome.ConfigFile{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, errors.New("invalid files")
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '[' {
		return nil, errors.New("files must be an array")
	}
	files := make([]memberhome.ConfigFile, 0, min(memberhome.ConfigImportMaxFiles, 16))
	var decodedTotal int64
	const maxEncodedFile = ((memberhome.ConfigImportMaxFileBytes + 2) / 3) * 4
	for dec.More() {
		if len(files) >= memberhome.ConfigImportMaxFiles {
			return nil, fmt.Errorf("import contains too many files")
		}
		var file protocol.ConfigImportFile
		if err = dec.Decode(&file); err != nil {
			return nil, errors.New("invalid file entry")
		}
		if len(file.ContentBase64) > maxEncodedFile {
			return nil, fmt.Errorf("file exceeds its size limit")
		}
		content, decodeErr := base64.StdEncoding.DecodeString(file.ContentBase64)
		if decodeErr != nil {
			return nil, errors.New("invalid content_base64")
		}
		if len(content) > memberhome.ConfigImportMaxFileBytes {
			return nil, fmt.Errorf("file exceeds its size limit")
		}
		decodedTotal += int64(len(content))
		if decodedTotal > memberhome.ConfigImportMaxBytes {
			return nil, fmt.Errorf("import exceeds its size limit")
		}
		files = append(files, memberhome.ConfigFile{Path: file.Path, Content: content, Mode: file.Mode})
	}
	end, err := dec.Token()
	if err != nil {
		return nil, errors.New("invalid files")
	}
	if delim, ok := end.(json.Delim); !ok || delim != ']' {
		return nil, errors.New("invalid files")
	}
	return files, nil
}

func configUnavailable() *protocol.Error {
	return &protocol.Error{Code: protocol.CodeUnavailable, Message: "config: persistent configuration is not enabled"}
}

func configError(err error) *protocol.Error {
	switch {
	case errors.Is(err, memberhome.ErrConfigConflict):
		return &protocol.Error{Code: protocol.CodeConflict, Message: "config: file changed; reload before saving"}
	case errors.Is(err, memberhome.ErrConfigNotFound), errors.Is(err, store.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return &protocol.Error{Code: protocol.CodeNotFound, Message: "config: path not found"}
	case errors.Is(err, memberhome.ErrConfigTooLarge):
		return invalidParams("config: file exceeds its size limit")
	case errors.Is(err, memberhome.ErrConfigBinary):
		return invalidParams("config: binary or invalid UTF-8 files cannot be saved")
	case errors.Is(err, memberhome.ErrConfigDenied):
		return &protocol.Error{Code: protocol.CodeDenied, Message: "config: path is not available"}
	default:
		// Preserve useful operating-system reasons without returning absolute
		// member-home paths embedded in PathError strings.
		return &protocol.Error{Code: protocol.CodeUnavailable, Message: "config: " + configFailureDetail(err)}
	}
}

func configFailureDetail(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "permission denied"):
		return "permission denied"
	case strings.Contains(message, "no space left on device"):
		return "no space left on device"
	case strings.Contains(message, "read-only file system"):
		return "read-only file system"
	case strings.Contains(message, "too many open files"):
		return "too many open files"
	case strings.Contains(message, "input/output error"):
		return "input/output error"
	default:
		return "persistent configuration operation failed"
	}
}
