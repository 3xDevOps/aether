package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/server"
	"github.com/3xDevOps/Aether/internal/store"
)

const memberUsage = "usage: aether-server member role <member-id> <viewer|collaborator|admin|super_admin> [--config <file>]"

func memberCmd(args []string) error {
	if len(args) < 3 || args[0] != "role" {
		return errors.New(memberUsage)
	}
	id, role := domain.MemberID(args[1]), domain.Role(args[2])
	o, _, rest, err := loadOptions("member role", args[3:], nil)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New(memberUsage)
	}
	db, err := store.Open(server.StorePath(*o.dataDir))
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // every change is committed before Close
	return memberRole(context.Background(), os.Stdout, db, id, role)
}

// memberRole sets a member's role with the store's SetMemberRole, as
// member.role does over RPC. The server reads roles on every call, so it
// applies at once.
func memberRole(ctx context.Context, w io.Writer, db *store.DB, id domain.MemberID, role domain.Role) error {
	if !role.Valid() {
		return fmt.Errorf("unknown role %q; want viewer, collaborator, admin, or super_admin", role)
	}
	m, err := db.GetMember(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no member %s: %w", id, err)
	}
	if err != nil {
		return err
	}
	was, err := db.SetMemberRole(ctx, id, role, "")
	var owns *store.OwnsWorkspacesError
	switch {
	case errors.Is(err, store.ErrLastSuperAdmin):
		return fmt.Errorf("refusing to demote the last approved super admin %s (%s); make another member super admin first", m.DisplayName, m.ID)
	case errors.As(err, &owns):
		return fmt.Errorf("refusing to demote %s (%s) to %s while they own workspaces %s; give each a new owner first: aether workspace owner <workspace> <member-id>",
			m.DisplayName, m.ID, role, strings.Join(owns.Workspaces, ", "))
	case err != nil:
		return err
	}
	_, _ = fmt.Fprintf(w, "%s (%s) is %s, was %s\n", m.DisplayName, m.ID, role, was)
	return nil
}
