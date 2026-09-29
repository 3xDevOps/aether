package edge

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	edgestore "github.com/3xDevOps/Aether/internal/edge/store"
)

// reauthWindow is how recently the browser deleting an account must
// itself have signed in with the provider. The account's last sign-in
// does not count: a sign-in in any browser refreshes it, so a stolen
// device token or session cookie could wait for one.
const reauthWindow = 5 * time.Minute

// transferCommand is the client command an admin of a server runs to
// transfer its ownership; the edge records the new owner only when the
// server reports it.
const transferCommand = "aether member transfer <member id>"

// confirmText is what a person types to confirm deleting a: the login,
// else the email, else, for an account the provider gave neither, its
// account id.
func confirmText(a edgeproto.AccountInfo) string {
	switch {
	case a.Login != "":
		return a.Login
	case a.Email != "":
		return a.Email
	}
	return a.ID
}

// confirms reports whether typed names a: its login or its email, in any
// case, or the account id of an account with neither.
func confirms(a edgeproto.AccountInfo, typed string) bool {
	typed = strings.TrimSpace(typed)
	switch {
	case typed == "":
		return false
	case a.Login == "" && a.Email == "":
		return typed == a.ID
	}
	return (a.Login != "" && strings.EqualFold(typed, a.Login)) || (a.Email != "" && strings.EqualFold(typed, a.Email))
}

// signedInRecently reports whether the browser v signed in with its
// provider within reauthWindow of now.
func (s *Service) signedInRecently(v visitor) bool {
	return s.now().Sub(v.SignedInAt) <= reauthWindow
}

func (s *Service) reauthURL(a edgeproto.AccountInfo) string {
	return s.signinOrigin + "/signin/" + a.Provider + "?" + url.Values{"next": {"/account"}}.Encode()
}

func (s *Service) summary(ctx context.Context, a edgeproto.AccountInfo) (edgeproto.AccountSummary, error) {
	rows, err := s.servers(ctx, a.Account)
	if err != nil {
		return edgeproto.AccountSummary{}, err
	}
	out := edgeproto.AccountSummary{Account: a, Owned: []edgeproto.ServerInfo{}, Member: []edgeproto.ServerInfo{}, Confirm: confirmText(a)}
	for _, row := range rows {
		switch {
		case row.Owner:
			out.Owned = append(out.Owned, row.ServerInfo)
		case row.Member:
			out.Member = append(out.Member, row.ServerInfo)
		}
	}
	return out, nil
}

// deleteAccount deletes the account of the browser v once confirm names
// it and v signed in with its provider within reauthWindow: its sessions,
// device tokens and live connections end, the servers it owns become
// ownerless, and every server it owned or belonged to is sent
// edgeproto.AccountDeleted, now or when it next connects.
func (s *Service) deleteAccount(ctx context.Context, v visitor, confirm string) error {
	a := v.Account
	if !s.signedInRecently(v) {
		return pageErr(http.StatusForbidden, "deleting an account needs a sign-in in this browser from the last %s: sign in again at %s, then delete it within %s",
			reauthWindow, s.reauthURL(a), reauthWindow)
	}
	if !confirms(a, confirm) {
		return pageErr(http.StatusBadRequest, "the confirmation does not name this account: type %s", confirmText(a))
	}
	d, err := s.store.DeleteAccount(ctx, a.Provider, a.Subject, s.now())
	if errors.Is(err, edgestore.ErrNotFound) {
		return pageErr(http.StatusNotFound, "account %s no longer exists at this edge", a.ID)
	}
	if err != nil {
		return err
	}
	s.link.AccountDeleted(a.Account, d.Notify)
	return nil
}

type accountPage struct {
	edgeproto.AccountSummary
	Fresh    bool
	Reauth   string
	Window   time.Duration
	Transfer string
}

func (s *Service) accountPage(w http.ResponseWriter, r *http.Request, v visitor) error {
	sum, err := s.summary(r.Context(), v.Account)
	if err != nil {
		return err
	}
	s.render(w, http.StatusOK, "account_page", s.view(&v, "Account", accountPage{
		AccountSummary: sum,
		Fresh:          s.signedInRecently(v),
		Reauth:         s.reauthURL(v.Account),
		Window:         reauthWindow,
		Transfer:       transferCommand,
	}))
	return nil
}

func (s *Service) accountDelete(w http.ResponseWriter, r *http.Request, v visitor) error {
	sum, err := s.summary(r.Context(), v.Account)
	if err != nil {
		return err
	}
	if err := s.deleteAccount(r.Context(), v, r.PostForm.Get("confirm")); err != nil {
		status := errorStatus(err)
		logFailure(r, status, err)
		page := s.view(&v, "Account", accountPage{
			AccountSummary: sum,
			Fresh:          s.signedInRecently(v),
			Reauth:         s.reauthURL(v.Account),
			Window:         reauthWindow,
			Transfer:       transferCommand,
		})
		page.Error = err.Error()
		s.render(w, status, "account_page", page)
		return nil
	}
	setCookie(w, sessionCookie, "", -1)
	s.render(w, http.StatusOK, "account_deleted", s.view(nil, "Account deleted", sum))
	return nil
}

func (s *Service) apiAccount(r *http.Request) (any, error) {
	c, _, err := s.authenticate(r)
	if err != nil {
		return nil, err
	}
	return s.summary(r.Context(), c.Account)
}
