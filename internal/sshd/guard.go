package sshd

import (
	"context"
	"encoding/json"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// targetResolver derives the permission target a request acts on from its
// raw params, before the handler runs. nil means the capability targets no
// run (launch, session administration) and the zero Target is checked.
type targetResolver func(s *Server, ctx context.Context, params json.RawMessage) (permissions.Target, *protocol.Error)

// runTarget resolves the standard "run_id" param to its run's ownership,
// protection, and session steer-others policy. It is the resolver for
// every method addressing one run.
func runTarget(s *Server, ctx context.Context, params json.RawMessage) (permissions.Target, *protocol.Error) {
	var p protocol.RunIDParams
	if len(params) != 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return permissions.Target{}, invalidParams("invalid params: " + err.Error())
		}
	}
	if p.RunID == "" {
		return permissions.Target{}, invalidParams("run_id is required")
	}
	target, err := resolveRunTarget(ctx, s.cfg.Store, domain.RunID(p.RunID))
	if err != nil {
		return permissions.Target{}, rpcError(err)
	}
	return target, nil
}

// registerGuarded adds a control-channel handler behind a capability
// check: the caller's role is re-fetched per request, resolve names the
// target (nil for none), and permissions.Check runs before h. Denials are
// CodeDenied and h never executes. Call from init() - this is the seam
// Wave 4 features register capability-checked methods through, in their
// own files, without editing a central dispatcher.
func registerGuarded(name string, cap permissions.Capability, resolve targetResolver, h methodHandler) {
	registerMethod(name, func(s *Server, ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
		actor, err := resolveActor(ctx, s.cfg.Store, member)
		if err != nil {
			return nil, rpcError(err)
		}
		target := permissions.Target{}
		if resolve != nil {
			var perr *protocol.Error
			if target, perr = resolve(s, ctx, params); perr != nil {
				return nil, perr
			}
		}
		if cerr := permissions.Check(cap, actor, target); cerr != nil {
			return nil, &protocol.Error{Code: protocol.CodeDenied, Message: name + ": " + cerr.Error()}
		}
		return h(s, ctx, member, params)
	})
}

// The Wave 1 run mutators, re-registered behind capability checks. Reads
// (run.list, run.get, run.pull) stay unguarded: View is universal.
func init() {
	registerGuarded(protocol.MethodRunLaunch, permissions.Launch, nil, (*Server).runLaunch)
	registerGuarded(protocol.MethodRunKill, permissions.Kill, runTarget, (*Server).runKill)
	registerGuarded(protocol.MethodRunClose, permissions.Kill, runTarget, (*Server).runClose)
	registerGuarded(protocol.MethodRunPause, permissions.Steer, runTarget, (*Server).runPause)
	registerGuarded(protocol.MethodRunResume, permissions.Steer, runTarget, (*Server).runResume)
	registerGuarded(protocol.MethodRunInject, permissions.Steer, runTarget, (*Server).runInject)
	registerGuarded(protocol.MethodRunRelaunch, permissions.Steer, runTarget, (*Server).runRelaunch)
	registerGuarded(protocol.MethodRunHandoff, permissions.Handoff, runTarget, (*Server).runHandoff)
}
