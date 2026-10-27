package sshd

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/watch"
)

// WatchService is the seam for watches. Satisfied by *watch.Service. Every
// method acts on the caller's own watches and preferences.
type WatchService interface {
	Create(ctx context.Context, member domain.MemberID, p protocol.WatchCreateParams) (protocol.WatchResult, error)
	List(ctx context.Context, member domain.MemberID, p protocol.WatchListParams) (protocol.WatchListResult, error)
	Get(ctx context.Context, member domain.MemberID, id string) (protocol.WatchGetResult, error)
	Update(ctx context.Context, member domain.MemberID, p protocol.WatchUpdateParams) (protocol.WatchResult, error)
	Delete(ctx context.Context, member domain.MemberID, id string) error
	Prefs(ctx context.Context, member domain.MemberID) (protocol.NotifyPrefs, error)
	SetPrefs(ctx context.Context, member domain.MemberID, p protocol.NotifyPrefs) (protocol.NotifyPrefs, error)
}

func init() {
	registerGuarded(protocol.MethodWatchCreate, permissions.View, nil, watchMethod(func(svc WatchService, ctx context.Context, m domain.MemberID, p protocol.WatchCreateParams) (any, error) {
		return svc.Create(ctx, m, p)
	}))
	registerGuarded(protocol.MethodWatchList, permissions.View, nil, watchMethod(func(svc WatchService, ctx context.Context, m domain.MemberID, p protocol.WatchListParams) (any, error) {
		return svc.List(ctx, m, p)
	}))
	registerGuarded(protocol.MethodWatchGet, permissions.View, nil, watchMethod(func(svc WatchService, ctx context.Context, m domain.MemberID, p protocol.WatchIDParams) (any, error) {
		return svc.Get(ctx, m, p.WatchID)
	}))
	registerGuarded(protocol.MethodWatchUpdate, permissions.View, nil, watchMethod(func(svc WatchService, ctx context.Context, m domain.MemberID, p protocol.WatchUpdateParams) (any, error) {
		return svc.Update(ctx, m, p)
	}))
	registerGuarded(protocol.MethodWatchDelete, permissions.View, nil, watchMethod(func(svc WatchService, ctx context.Context, m domain.MemberID, p protocol.WatchIDParams) (any, error) {
		return struct{}{}, svc.Delete(ctx, m, p.WatchID)
	}))
	registerGuarded(protocol.MethodNotifyPrefsGet, permissions.View, nil, watchMethod(func(svc WatchService, ctx context.Context, m domain.MemberID, _ struct{}) (any, error) {
		return svc.Prefs(ctx, m)
	}))
	registerGuarded(protocol.MethodNotifyPrefsSet, permissions.View, nil, watchMethod(func(svc WatchService, ctx context.Context, m domain.MemberID, p protocol.NotifyPrefs) (any, error) {
		return svc.SetPrefs(ctx, m, p)
	}))
}

func watchMethod[P any](call func(WatchService, context.Context, domain.MemberID, P) (any, error)) methodHandler {
	return func(s *Server, ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
		svc := s.cfg.Services.Watches
		if svc == nil {
			return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "watch service not configured"}
		}
		p, perr := decodeParams[P](raw)
		if perr != nil {
			return nil, perr
		}
		out, err := call(svc, ctx, member, p)
		switch {
		case errors.Is(err, watch.ErrInvalid):
			return nil, invalidParams(err.Error())
		case errors.Is(err, watch.ErrDenied):
			return nil, &protocol.Error{Code: protocol.CodeDenied, Message: err.Error()}
		case err != nil:
			return nil, rpcError(err)
		}
		return out, nil
	}
}
