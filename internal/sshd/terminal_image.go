package sshd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/gif"
	"image/jpeg"
	"image/png"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"golang.org/x/image/webp"
)

const (
	maxTerminalImageBytes        = 8 << 20
	maxTerminalImageEncodedBytes = 4 * ((maxTerminalImageBytes + 2) / 3)
)

func init() {
	registerMethod(protocol.MethodTerminalImage, (*Server).terminalImage)
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

	return protocol.TerminalImageResult{Path: path}, nil
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
	if len(data) == 0 {
		return nil, "", fmt.Errorf("image content is empty")
	}
	if len(data) > maxTerminalImageBytes {
		return nil, "", fmt.Errorf("image exceeds the 8 MiB limit")
	}

	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		if _, err := png.DecodeConfig(bytes.NewReader(data)); err != nil {
			return nil, "", fmt.Errorf("invalid PNG image: %w", err)
		}
		return data, ".png", nil
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		if _, err := jpeg.DecodeConfig(bytes.NewReader(data)); err != nil {
			return nil, "", fmt.Errorf("invalid JPEG image: %w", err)
		}
		return data, ".jpg", nil
	case len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))):
		if _, err := gif.DecodeConfig(bytes.NewReader(data)); err != nil {
			return nil, "", fmt.Errorf("invalid GIF image: %w", err)
		}
		return data, ".gif", nil
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		if _, err := webp.DecodeConfig(bytes.NewReader(data)); err != nil {
			return nil, "", fmt.Errorf("invalid WebP image: %w", err)
		}
		return data, ".webp", nil
	default:
		return nil, "", fmt.Errorf("unsupported or invalid image format")
	}
}
