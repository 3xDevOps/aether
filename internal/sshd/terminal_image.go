package sshd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	maxTerminalImageEncodedBytes = 4 * ((domain.MaxImageBytes + 2) / 3)
	maxTerminalFileEncodedBytes  = 4 * ((memberhome.ConfigMaxFileBytes + 2) / 3)
)

func init() {
	registerMethod(protocol.MethodTerminalImage, (*Server).terminalImage)
	registerMethod(protocol.MethodTerminalFile, (*Server).terminalFile)
}

func (s *Server) terminalImage(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	params, perr := decodeParams[protocol.TerminalImageParams](raw)
	if perr != nil {
		return nil, perr
	}

	run := domain.RunID(params.RunID)
	if run != "" {
		if err := checkSteer(ctx, s.cfg.Store, member, run); err != nil {
			return nil, rpcError(err)
		}
	}
	data, extension, err := decodeTerminalImage(params.Content)
	if err != nil {
		return nil, invalidParams(err.Error())
	}
	if s.cfg.Runs == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "terminal.image: image upload is not available"}
	}
	path, err := s.cfg.Runs.SaveTerminalImage(ctx, member, run, extension, data)
	if err != nil {
		return nil, rpcError(err)
	}

	return protocol.TerminalUploadResult{Path: path}, nil
}

func (s *Server) terminalFile(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	params, perr := decodeParams[protocol.TerminalFileParams](raw)
	if perr != nil {
		return nil, perr
	}

	run := domain.RunID(params.RunID)
	if run != "" {
		if err := checkSteer(ctx, s.cfg.Store, member, run); err != nil {
			return nil, rpcError(err)
		}
	}
	data, err := decodeTerminalFile(params.Content)
	if err != nil {
		return nil, invalidParams(err.Error())
	}
	if s.cfg.Runs == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "terminal.file: file upload is not available"}
	}
	path, err := s.cfg.Runs.SaveTerminalFile(ctx, member, run, params.Name, data)
	if err != nil {
		return nil, rpcError(err)
	}

	return protocol.TerminalUploadResult{Path: path}, nil
}

func decodeTerminalImage(encoded string) ([]byte, string, error) {
	if encoded == "" {
		return nil, "", fmt.Errorf("image content is required")
	}
	if len(encoded) > maxTerminalImageEncodedBytes {
		return nil, "", fmt.Errorf("image exceeds the 8 MiB limit")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, "", fmt.Errorf("image content is not valid base64: %w", err)
	}
	extension, _, err := memberhome.ValidateImage(data)
	if err != nil {
		return nil, "", err
	}
	return data, extension, nil
}

func decodeTerminalFile(encoded string) ([]byte, error) {
	if len(encoded) > maxTerminalFileEncodedBytes {
		return nil, fmt.Errorf("file exceeds the 64 MiB limit")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("file content is not valid base64: %w", err)
	}
	return data, nil
}
