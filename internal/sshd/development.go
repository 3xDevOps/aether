package sshd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
)

// DevelopmentService is the authenticated human entry point to the same run
// development broker used by socket-bound agents. authorize must be called at
// physical admission, not merely before queuing work. The principal is supplied
// by the transport and is never decoded from caller JSON.
type DevelopmentService interface {
	Call(context.Context, domain.Run, control.Principal, string, json.RawMessage, func(context.Context) error) (any, error)
	AttachTerminal(context.Context, domain.Run, control.Principal, protocol.DevTerminalTarget, protocol.DevControlFence, ptyhost.AttachClient, io.ReadWriter, <-chan [2]uint, func(context.Context) error) error
	BrowserFrames(context.Context, domain.Run, control.Principal, protocol.DevBrowserPageTarget, func(context.Context) error) (<-chan protocol.DevBrowserFrame, func(), error)
	OpenArtifact(context.Context, domain.Run, control.Principal, string, func(context.Context) error) (protocol.DevArtifact, io.ReadCloser, error)
}

func init() {
	registerDevelopment[protocol.DevTerminalListParams](protocol.MethodDevTerminalList)
	registerDevelopment[protocol.DevTerminalStartParams](protocol.MethodDevTerminalStart)
	registerDevelopment[protocol.DevTerminalOutputParams](protocol.MethodDevTerminalOutput)
	registerDevelopment[protocol.DevTerminalScreenParams](protocol.MethodDevTerminalScreen)
	registerDevelopment[protocol.DevTerminalScreenshotParams](protocol.MethodDevTerminalScreenshot)
	registerDevelopment[protocol.DevTerminalInputParams](protocol.MethodDevTerminalInput)
	registerDevelopment[protocol.DevTerminalResizeParams](protocol.MethodDevTerminalResize)
	registerDevelopment[protocol.DevTerminalWaitParams](protocol.MethodDevTerminalWait)
	registerDevelopment[protocol.DevTerminalStopParams](protocol.MethodDevTerminalStop)
	registerDevelopment[protocol.DevBrowserStatusParams](protocol.MethodDevBrowserStatus)
	registerDevelopment[protocol.DevBrowserOpenParams](protocol.MethodDevBrowserOpen)
	registerDevelopment[protocol.DevBrowserPagesParams](protocol.MethodDevBrowserPages)
	registerDevelopment[protocol.DevBrowserNavigateParams](protocol.MethodDevBrowserNavigate)
	registerDevelopment[protocol.DevBrowserSnapshotParams](protocol.MethodDevBrowserSnapshot)
	registerDevelopment[protocol.DevBrowserActionParams](protocol.MethodDevBrowserAction)
	registerDevelopment[protocol.DevBrowserScreenshotParams](protocol.MethodDevBrowserScreenshot)
	registerDevelopment[protocol.DevBrowserViewportParams](protocol.MethodDevBrowserViewport)
	registerDevelopment[protocol.DevBrowserWaitParams](protocol.MethodDevBrowserWait)
	registerDevelopment[protocol.DevBrowserConsoleParams](protocol.MethodDevBrowserConsole)
	registerDevelopment[protocol.DevBrowserNetworkParams](protocol.MethodDevBrowserNetwork)
	registerDevelopment[protocol.DevBrowserResetParams](protocol.MethodDevBrowserReset)
	registerDevelopment[protocol.DevBrowserCloseParams](protocol.MethodDevBrowserClose)
	registerDevelopment[protocol.DevControlStatusParams](protocol.MethodDevControlStatus)
	registerDevelopment[protocol.DevControlAcquireParams](protocol.MethodDevControlAcquire)
	registerDevelopment[protocol.DevControlReleaseParams](protocol.MethodDevControlRelease)
	registerDevelopment[protocol.DevArtifactListParams](protocol.MethodDevArtifactList)
	registerDevelopment[protocol.DevArtifactGetParams](protocol.MethodDevArtifactGet)
	registerDevelopment[protocol.DevArtifactDeleteParams](protocol.MethodDevArtifactDelete)
	registerDevelopment[protocol.DevArtifactRetainParams](protocol.MethodDevArtifactRetain)
}

func decodeDevelopment(raw json.RawMessage, dst any) *protocol.Error {
	if len(raw) == 0 || len(raw) > protocol.MaxDevParamsBytes || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return invalidParams("development params must be a bounded JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return invalidParams("invalid development params: " + err.Error())
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return invalidParams("development params must contain one JSON object")
	}
	return nil
}

func registerDevelopment[T any](method string) {
	registerGuarded(method, permissions.Steer, runTarget, func(s *Server, ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
		var params T
		if err := decodeDevelopment(raw, &params); err != nil {
			return nil, err
		}
		var target protocol.DevRunParams
		if err := json.Unmarshal(raw, &target); err != nil {
			return nil, invalidParams(err.Error())
		}
		run, principal, authorize, authorityErr := s.developmentAuthority(ctx, member, target.RunID)
		if authorityErr != nil {
			return nil, rpcError(authorityErr)
		}
		result, callErr := s.cfg.Services.Development.Call(ctx, *run, principal, method, raw, authorize)
		if callErr != nil {
			return nil, rpcError(callErr)
		}
		if authorizationErr := authorize(ctx); authorizationErr != nil {
			return nil, rpcError(authorizationErr)
		}
		bounded, marshalErr := protocol.MarshalDevResult(result)
		if marshalErr != nil {
			return nil, rpcError(marshalErr)
		}
		return bounded, nil
	})
}

func (s *Server) developmentAuthority(ctx context.Context, member domain.MemberID, runID string) (*domain.Run, control.Principal, func(context.Context) error, error) {
	principal := control.Principal{Kind: control.PrincipalMember, MemberID: member}
	if runID == "" {
		return nil, principal, nil, invalidParams("run_id is required")
	}
	authorize := func(ctx context.Context) error {
		if err := s.checkMember(ctx, member); err != nil {
			return err
		}
		if err := checkSteer(ctx, s.cfg.Store, member, domain.RunID(runID)); err != nil {
			return err
		}
		current, err := s.cfg.Store.GetRun(ctx, domain.RunID(runID))
		if err != nil {
			return err
		}
		_, err = ResolveLaunchAccount(ctx, s.cfg.Store, member, string(current.AccountMember()))
		return err
	}
	if err := authorize(ctx); err != nil {
		return nil, principal, nil, err
	}
	if s.cfg.Services.Development == nil {
		return nil, principal, nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "development service unavailable"}
	}
	run, err := s.cfg.Store.GetRun(ctx, domain.RunID(runID))
	return run, principal, authorize, err
}
