package localgw

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func (g *Gateway) localWorkspaceSelection(r *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		WorkspaceID *string `json:"workspace_id"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	cfg := g.local.snapshot()
	raw, perr := g.cfg.Backend.Call(r.Context(), protocol.MethodServerInfo, json.RawMessage(`{}`))
	if perr != nil {
		return nil, perr
	}
	var info protocol.ServerInfoResult
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, localError(fmt.Errorf("workspace.selection: decode server.info: %w", err))
	}
	configPath, err := cli.Path()
	if err != nil {
		return nil, localError(err)
	}
	key := sha256.Sum256([]byte(cfg.Addr + "\x00" + info.Member.ID))
	path := filepath.Join(filepath.Dir(configPath), "workspace-selection", fmt.Sprintf("%x.json", key))
	result := struct {
		WorkspaceID string `json:"workspace_id"`
	}{}
	if params.WorkspaceID == nil {
		data, readErr := os.ReadFile(path)
		if os.IsNotExist(readErr) {
			return result, nil
		}
		if readErr != nil {
			return nil, localError(fmt.Errorf("workspace.selection: read: %w", readErr))
		}
		if err = json.Unmarshal(data, &result); err != nil {
			return nil, localError(fmt.Errorf("workspace.selection: decode: %w", err))
		}
		return result, nil
	}
	result.WorkspaceID = *params.WorkspaceID
	data, err := json.Marshal(result)
	if err != nil {
		return nil, localError(err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, localError(fmt.Errorf("workspace.selection: create directory: %w", err))
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".selection-*")
	if err != nil {
		return nil, localError(fmt.Errorf("workspace.selection: create file: %w", err))
	}
	defer func() { _ = os.Remove(file.Name()) }()
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return nil, localError(fmt.Errorf("workspace.selection: write: %w", writeErr))
	}
	if closeErr != nil {
		return nil, localError(fmt.Errorf("workspace.selection: close: %w", closeErr))
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return nil, localError(fmt.Errorf("workspace.selection: replace: %w", err))
	}
	return result, nil
}

func localError(err error) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
}
