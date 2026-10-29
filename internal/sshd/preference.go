package sshd

import (
	"context"
	"encoding/json"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// PreferenceService is the seam for private assistant preferences.
// Satisfied by *preference.Service. Every method acts on the caller's own
// preferences; another member's id reads as not found.
type PreferenceService interface {
	List(ctx context.Context, member domain.MemberID, p protocol.PreferenceListParams) (protocol.PreferenceListResult, error)
	Add(ctx context.Context, member domain.MemberID, text, workspaceID, source, ref string) (protocol.PreferenceResult, error)
	Update(ctx context.Context, member domain.MemberID, p protocol.PreferenceUpdateParams, source, ref string) (protocol.PreferenceResult, error)
	Delete(ctx context.Context, member domain.MemberID, id string, shown time.Time) (protocol.PreferenceDeleteResult, error)
	// DeleteMember deletes the member together with their preferences.
	DeleteMember(ctx context.Context, member domain.MemberID) error
}

func init() {
	registerGuarded(protocol.MethodPreferenceList, permissions.View, nil, preferenceMethod(func(svc PreferenceService, ctx context.Context, m domain.MemberID, p protocol.PreferenceListParams) (any, error) {
		return svc.List(ctx, m, p)
	}))
	registerGuarded(protocol.MethodPreferenceAdd, permissions.View, nil, preferenceMethod(func(svc PreferenceService, ctx context.Context, m domain.MemberID, p protocol.PreferenceAddParams) (any, error) {
		return svc.Add(ctx, m, p.Text, p.WorkspaceID, protocol.PreferenceSourceSettings, "")
	}))
	registerGuarded(protocol.MethodPreferenceUpdate, permissions.View, nil, preferenceMethod(func(svc PreferenceService, ctx context.Context, m domain.MemberID, p protocol.PreferenceUpdateParams) (any, error) {
		return svc.Update(ctx, m, p, protocol.PreferenceSourceSettings, "")
	}))
	registerGuarded(protocol.MethodPreferenceDelete, permissions.View, nil, preferenceMethod(func(svc PreferenceService, ctx context.Context, m domain.MemberID, p protocol.PreferenceIDParams) (any, error) {
		return svc.Delete(ctx, m, p.ID, time.Time{})
	}))
}

// preferenceMethod serves a preference method to the member in person: a
// connector, which acts for an app holding the member's grant, is refused
// whatever its scopes.
func preferenceMethod[P any](call func(PreferenceService, context.Context, domain.MemberID, P) (any, error)) methodHandler {
	return func(s *Server, ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
		if id, ok := ctx.Value(connIdentityKey{}).(connIdentity); (ok && id.connector != "") || domain.ViaFrom(ctx) != "" {
			return nil, &protocol.Error{Code: protocol.CodeDenied, Message: "preferences are private: only you can read or change them, in the Aether app"}
		}
		svc := s.cfg.Services.Preferences
		if svc == nil {
			return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "preference service not configured"}
		}
		p, perr := decodeParams[P](raw)
		if perr != nil {
			return nil, perr
		}
		out, err := call(svc, ctx, member, p)
		if err != nil {
			return nil, rpcError(err)
		}
		return out, nil
	}
}
