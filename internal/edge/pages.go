package edge

import (
	"crypto/rand"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/edge/edgestore"
	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// User codes are 8 letters without vowels, shown as XXXX-XXXX: 20^8, about
// 2.6e10, codes. They are entered only through the confirmation form,
// which a signed-in account may use about 20 times in a code's 10-minute
// life, so an account guessing codes finds one of a thousand live ones
// with odds below one in a million.
const (
	userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"
	userCodeLength   = 8
)

func newUserCode() string {
	b := make([]byte, userCodeLength)
	for i := range b {
		b[i] = userCodeAlphabet[randIndex(len(userCodeAlphabet))]
	}
	return string(b[:4]) + "-" + string(b[4:])
}

// randIndex draws uniformly from [0, n) by rejection, n <= 256.
func randIndex(n int) int {
	limit := 256 - 256%n
	var b [1]byte
	for {
		_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error.
		if int(b[0]) < limit {
			return int(b[0]) % n
		}
	}
}

// normalizeUserCode accepts a user code as a person typed it: any case,
// with or without the hyphen and spaces.
func normalizeUserCode(code string) (string, bool) {
	code = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code))
	if len(code) != userCodeLength {
		return "", false
	}
	for i := 0; i < len(code); i++ {
		if !strings.ContainsRune(userCodeAlphabet, rune(code[i])) {
			return "", false
		}
	}
	return code, true
}

func keyFingerprint(line string) string {
	key, err := edgeproto.ParseDeviceKey(line)
	if err != nil {
		return ""
	}
	return ssh.FingerprintSHA256(key)
}

func (s *Service) signout(w http.ResponseWriter, r *http.Request, v visitor) error {
	if err := s.store.DeleteSession(r.Context(), v.ID); err != nil {
		return err
	}
	setCookie(w, sessionCookie, "", -1)
	http.Redirect(w, r, "/signin", http.StatusSeeOther)
	return nil
}

func (s *Service) devicePage(w http.ResponseWriter, r *http.Request, v visitor) error {
	s.render(w, http.StatusOK, "device", s.view(&v, "Sign in a device", nil))
	return nil
}

type deviceConfirm struct {
	UserCode    string
	Label       string
	Fingerprint string
	Started     time.Duration
	// From is the address the sign-in came from; Here is the address of
	// the browser confirming it.
	From, Here string
}

// deviceLookup finds the pending authorization for a typed user code and
// asks the person to confirm it.
func (s *Service) deviceLookup(w http.ResponseWriter, r *http.Request, v visitor) error {
	a, code, err := s.pendingDevice(r, v)
	if err != nil {
		status := errorStatus(err)
		logFailure(r, status, err)
		page := s.view(&v, "Sign in a device", nil)
		page.Error = err.Error()
		s.render(w, status, "device", page)
		return nil
	}
	s.render(w, http.StatusOK, "device_confirm", s.view(&v, "Confirm this device", deviceConfirm{
		UserCode:    code[:4] + "-" + code[4:],
		Label:       a.Label,
		Fingerprint: keyFingerprint(a.Key),
		Started:     s.now().Sub(a.CreatedAt).Round(time.Second),
		From:        a.ClientAddr,
		Here:        clientAddr(r).String(),
	}))
	return nil
}

func (s *Service) pendingDevice(r *http.Request, v visitor) (edgestore.DeviceAuth, string, error) {
	if !s.codeLimit.allow(addrKeys(r)...) || !s.codeAccountLimit.allow(v.AccountID) {
		return edgestore.DeviceAuth{}, "", edgeproto.RefusalTooMany
	}
	code, ok := normalizeUserCode(r.PostForm.Get("user_code"))
	if !ok {
		return edgestore.DeviceAuth{}, "", pageErr(http.StatusBadRequest,
			"a code has 8 letters, like BCDF-GHJK; check what your terminal shows")
	}
	a, err := s.store.PendingDeviceAuth(r.Context(), edgeproto.HashToken(code), s.now())
	if errors.Is(err, edgestore.ErrNotFound) {
		return edgestore.DeviceAuth{}, "", pageErr(http.StatusNotFound,
			"no sign-in is waiting for code %s; codes expire after %s, so run aether login again", code, deviceCodeTTL)
	}
	return a, code, err
}

func (s *Service) deviceDecide(w http.ResponseWriter, r *http.Request, v visitor) error {
	a, code, err := s.pendingDevice(r, v)
	if err != nil {
		return err
	}
	approve := r.PostForm.Get("decision") == "approve"
	err = s.store.DecideDeviceAuth(r.Context(), a.UserCodeHash, v.AccountID, approve, s.now())
	if errors.Is(err, edgestore.ErrNotFound) {
		return pageErr(http.StatusNotFound, "the sign-in for code %s ended before you confirmed it; run aether login again", code)
	}
	if err != nil {
		return err
	}
	title := "Device denied"
	if approve {
		title = "Device signed in"
	}
	s.render(w, http.StatusOK, "device_done", s.view(&v, title, struct {
		Approved bool
		Label    string
	}{approve, a.Label}))
	return nil
}

func (s *Service) serversPage(w http.ResponseWriter, r *http.Request, v visitor) error {
	rows, err := s.servers(r.Context(), v.Account)
	if err != nil {
		return err
	}
	s.render(w, http.StatusOK, "servers", s.view(&v, "Servers", rows))
	return nil
}

func (s *Service) serverRemove(w http.ResponseWriter, r *http.Request, v visitor) error {
	id := r.PostForm.Get("server")
	srv, err := s.store.Server(r.Context(), id)
	if errors.Is(err, edgestore.ErrNotFound) {
		return edgeproto.RefusalUnknownServer
	}
	if err != nil {
		return err
	}
	if srv.Owner.Provider != v.Account.Provider || srv.Owner.Subject != v.Account.Subject {
		return pageErr(http.StatusForbidden, "only the owner of %s can remove it", id)
	}
	if err := s.RemoveServer(r.Context(), id); err != nil {
		return err
	}
	s.link.Unenroll(id)
	http.Redirect(w, r, "/servers", http.StatusSeeOther)
	return nil
}

func (s *Service) addServerPage(w http.ResponseWriter, r *http.Request, v visitor) error {
	s.render(w, http.StatusOK, "add_server", s.view(&v, "Add a server", nil))
	return nil
}

func (s *Service) addServer(w http.ResponseWriter, r *http.Request, v visitor) error {
	var res edgeproto.ClaimResponse
	err := error(edgeproto.RefusalTooMany)
	if s.claimLimit.allow(addrKeys(r)...) {
		res, err = s.claim(r.Context(), r.PostForm.Get("code"), v.Account,
			edgeproto.Device{ID: v.ID, Label: "browser"})
	}
	if err != nil {
		status := errorStatus(err)
		logFailure(r, status, err)
		page := s.view(&v, "Add a server", nil)
		page.Error = err.Error()
		s.render(w, status, "add_server", page)
		return nil
	}
	s.render(w, http.StatusOK, "claimed", s.view(&v, "Server added", res))
	return nil
}

func (s *Service) devicesPage(w http.ResponseWriter, r *http.Request, v visitor) error {
	devices, err := s.store.Devices(r.Context(), v.AccountID)
	if err != nil {
		return err
	}
	s.render(w, http.StatusOK, "devices", s.view(&v, "Devices", devices))
	return nil
}

func (s *Service) deviceRevoke(w http.ResponseWriter, r *http.Request, v visitor) error {
	if err := s.revokeDevice(r.Context(), v.AccountID, r.PostForm.Get("device")); err != nil {
		return err
	}
	http.Redirect(w, r, "/devices", http.StatusSeeOther)
	return nil
}

// authorizeRequest is a server's web sign-in request, from the query of
// GET /authorize or the fields of the form that confirms it.
type authorizeRequest struct {
	Server    string
	State     string
	Challenge string
	App       bool
}

func parseAuthorize(q url.Values) (authorizeRequest, error) {
	req := authorizeRequest{
		Server:    q.Get(edgeproto.ParamServer),
		State:     q.Get(edgeproto.ParamState),
		Challenge: q.Get(edgeproto.ParamChallenge),
	}
	switch q.Get(edgeproto.ParamReturn) {
	case "":
	case edgeproto.ReturnApp:
		req.App = true
	default:
		return authorizeRequest{}, pageErr(http.StatusBadRequest, "return must be empty or %q", edgeproto.ReturnApp)
	}
	switch {
	case !edgeproto.ValidServerID(req.Server):
		return authorizeRequest{}, pageErr(http.StatusBadRequest, "server %q is not a server id", req.Server)
	case !validState(req.State):
		return authorizeRequest{}, pageErr(http.StatusBadRequest, "state must be 1 to 256 URL-safe characters")
	case !edgeproto.ValidToken(req.Challenge):
		return authorizeRequest{}, pageErr(http.StatusBadRequest, "challenge must be a PKCE S256 challenge")
	}
	return req, nil
}

func validState(state string) bool {
	if state == "" || len(state) > 256 {
		return false
	}
	for i := 0; i < len(state); i++ {
		c := state[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && !strings.ContainsRune("-._~", rune(c)) {
			return false
		}
	}
	return true
}

// callbackURL is where a web sign-in code goes: computed from the
// configured server domain and the server id, never from the request.
func (s *Service) callbackURL(req authorizeRequest, code string) string {
	q := url.Values{edgeproto.ParamCode: {code}, edgeproto.ParamState: {req.State}}.Encode()
	if req.App {
		return edgeproto.AppCallbackURL + "?" + q
	}
	return "https://" + edgeproto.ServerHostname(req.Server, s.serverDomain) + edgeproto.PathAuthCallback + "?" + q
}

type authorizeView struct {
	authorizeRequest
	Name     string
	Hostname string
}

func (s *Service) authorizePage(w http.ResponseWriter, r *http.Request) {
	req, err := parseAuthorize(r.URL.Query())
	if err != nil {
		s.fail(w, r, nil, err)
		return
	}
	s.signedIn(func(w http.ResponseWriter, r *http.Request, v visitor) error {
		if _, err := s.Admit(r.Context(), req.Server, v.Account); err != nil {
			return err
		}
		srv, err := s.store.Server(r.Context(), req.Server)
		if err != nil {
			return err
		}
		s.render(w, http.StatusOK, "authorize", s.view(&v, "Sign in to "+srv.Name, authorizeView{
			authorizeRequest: req,
			Name:             srv.Name,
			Hostname:         edgeproto.ServerHostname(req.Server, s.serverDomain),
		}))
		return nil
	})(w, r)
}

// authorize issues a one-time code bound to the server, this account and
// session, and the server's PKCE challenge, and sends the browser back.
func (s *Service) authorize(w http.ResponseWriter, r *http.Request, v visitor) error {
	req, err := parseAuthorize(r.PostForm)
	if err != nil {
		return err
	}
	if _, err := s.Admit(r.Context(), req.Server, v.Account); err != nil {
		return err
	}
	code := edgeproto.NewToken()
	now := s.now()
	if err := s.store.CreateWebCode(r.Context(), edgestore.WebCode{
		CodeHash:  edgeproto.HashToken(code),
		ServerID:  req.Server,
		AccountID: v.AccountID,
		SessionID: v.ID,
		Challenge: req.Challenge,
		ExpiresAt: now.Add(webCodeTTL),
	}, now); err != nil {
		return err
	}
	http.Redirect(w, r, s.callbackURL(req, code), http.StatusSeeOther)
	return nil
}
