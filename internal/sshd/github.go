package sshd

import (
	"context"
	"encoding/json"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerMethod(protocol.MethodGitHubConnect, (*Server).githubConnect)
	registerMethod(protocol.MethodGitHubProbe, (*Server).githubProbe)
}

// githubConnect finishes the caller's own GitHub connection. It is member
// scoped for the same reason the environment terminal is: the login it
// completes lives in that member's container and nowhere else.
func (s *Server) githubConnect(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	conn, err := s.cfg.Runs.ConnectGitHub(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.GitHubConnectResult{
		Login:       conn.Login,
		SigningKey:  conn.SigningKey,
		Fingerprint: conn.Fingerprint,
	}, nil
}

// githubProbe reports the gh in the caller's own environment terminal, so
// the dashboard can say what is wrong before it prints a login command that
// container cannot run.
func (s *Server) githubProbe(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	cli, err := s.cfg.Runs.ProbeGitHubCLI(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.GitHubProbeResult{
		Status:      string(cli.Status),
		Version:     cli.Version,
		Minimum:     cli.Minimum,
		Detail:      cli.Detail,
		Image:       cli.Image,
		SavedImage:  cli.SavedImage,
		Path:        cli.Path,
		Remedy:      cli.Remedy,
		AdminRemedy: cli.AdminRemedy,
	}, nil
}
