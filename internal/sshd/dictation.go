package sshd

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// DictationService is the seam for server-side dictation. Satisfied by
// *dictation.Service.
type DictationService interface {
	Installed() bool
	Transcribe(ctx context.Context, member domain.MemberID, wav []byte, hints []string) (string, error)
}

func init() {
	registerGuarded(protocol.MethodDictationTranscribe, permissions.View, nil, (*Server).dictationTranscribe)
}

func (s *Server) dictationInstalled() bool {
	return s.cfg.Services.Dictation != nil && s.cfg.Services.Dictation.Installed()
}

func (s *Server) dictationTranscribe(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc := s.cfg.Services.Dictation
	if svc == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "dictation service not configured"}
	}
	p, perr := decodeParams[protocol.DictationTranscribeParams](raw)
	if perr != nil {
		return nil, perr
	}
	wav, err := base64.StdEncoding.DecodeString(p.AudioBase64)
	if err != nil {
		return nil, invalidParams("audio_base64 is not standard base64: " + err.Error())
	}
	text, err := svc.Transcribe(ctx, member, wav, p.Hints)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.DictationTranscribeResult{Text: text}, nil
}
