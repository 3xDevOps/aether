package localgw

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/3xDevOps/Aether/internal/cli"
	edgeclient "github.com/3xDevOps/Aether/internal/edge/client"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// States of the gateway's edge sign-in.
const (
	loginPending  = "pending"
	loginSignedIn = "signed_in"
	loginFailed   = "failed"
)

// loginView is the gateway's edge sign-in as edge.login and edge.status
// report it: Edge is the relay origin and SigninOrigin the origin whose
// page confirms the code. The device code the gateway polls with and the
// device token it receives are never part of it.
type loginView struct {
	State           string                 `json:"state"`
	Edge            string                 `json:"edge"`
	SigninOrigin    string                 `json:"signin_origin"`
	UserCode        string                 `json:"user_code"`
	VerificationURI string                 `json:"verification_uri"`
	Account         *edgeproto.AccountInfo `json:"account,omitempty"`
	Error           string                 `json:"error,omitempty"`
}

// edgeLogin is the one edge sign-in the gateway runs at a time. A new
// edge.login replaces the one in progress, whose code the person may
// never have entered.
type edgeLogin struct {
	mu      sync.Mutex
	current *loginAttempt
	// waits counts the goroutines polling the edge, so Close can wait
	// for their token writes to finish.
	waits sync.WaitGroup
}

type loginAttempt struct {
	cancel context.CancelFunc
	view   loginView
}

// begin waits for l in the background, replacing any sign-in in
// progress. parent bounds the wait beside the edge's own expiry.
func (e *edgeLogin) begin(parent context.Context, client *edgeclient.Client, l *edgeclient.Login) loginView {
	ctx, cancel := context.WithCancel(parent)
	a := &loginAttempt{cancel: cancel, view: loginView{
		State: loginPending, Edge: client.URL(), SigninOrigin: l.SigninOrigin, UserCode: l.UserCode, VerificationURI: l.VerificationURI,
	}}
	e.mu.Lock()
	if e.current != nil {
		e.current.cancel()
	}
	e.current = a
	view := a.view
	e.mu.Unlock()
	e.waits.Go(func() {
		defer cancel()
		session, err := client.Wait(ctx, l)
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.current != a {
			return
		}
		if err != nil {
			a.view.State, a.view.Error = loginFailed, err.Error()
			return
		}
		a.view.State, a.view.Account = loginSignedIn, &session.Account
	})
	return view
}

// view is the sign-in in progress or last finished, nil when there is none.
func (e *edgeLogin) view() *loginView {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.current == nil {
		return nil
	}
	view := e.current.view
	return &view
}

// forget drops the sign-in on origin, stopping it if it is still waiting.
func (e *edgeLogin) forget(origin string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.current != nil && e.current.view.Edge == origin {
		e.current.cancel()
		e.current = nil
	}
}

// chooseEdge is the client for the edge a verb names, or for the only
// edge this machine is signed in to, else the project's edge.
func chooseEdge(edgeURL string) (*edgeclient.Client, *protocol.Error) {
	dir, err := cli.Dir()
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	client, err := edgeclient.Choose(dir, edgeURL)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()}
	}
	return client, nil
}

// edgeError maps an edge client failure onto the wire in its own words:
// one the person fixes, by signing in or correcting what they entered,
// is invalid state; an edge that cannot be reached or fails is
// unavailable.
func edgeError(err error) *protocol.Error {
	var refused *edgeclient.RefusedError
	if errors.Is(err, edgeclient.ErrNotSignedIn) || (errors.As(err, &refused) && refused.Status < http.StatusInternalServerError) {
		return &protocol.Error{Code: protocol.CodeInvalidState, Message: err.Error()}
	}
	return &protocol.Error{Code: protocol.CodeUnavailable, Message: err.Error()}
}

// localEdgeLogin starts a device sign-in, as aether login does, and
// answers the address and code the person confirms at the edge.
func (g *Gateway) localEdgeLogin(r *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		Edge  string `json:"edge"`
		Label string `json:"label"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	client, perr := chooseEdge(params.Edge)
	if perr != nil {
		return nil, perr
	}
	if params.Label == "" {
		host, err := os.Hostname()
		if err != nil {
			return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "name this device with label: " + err.Error()}
		}
		params.Label = host
	}
	login, err := client.StartLogin(r.Context(), params.Label)
	if err != nil {
		return nil, edgeError(err)
	}
	return g.local.edge.begin(g.ctx, client, login), nil
}

// signedInEdge is one edge this machine holds a device token for, by its
// relay origin, with the sign-in origin that issued the token.
type signedInEdge struct {
	Edge         string                 `json:"edge"`
	SigninOrigin string                 `json:"signin_origin,omitempty"`
	Account      *edgeproto.AccountInfo `json:"account,omitempty"`
	Device       *edgeproto.Device      `json:"device,omitempty"`
	Error        string                 `json:"error,omitempty"`
}

func (g *Gateway) localEdgeStatus(*http.Request, []byte) (any, *protocol.Error) {
	dir, err := cli.Dir()
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	origins, err := edgeclient.SignedIn(dir)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	edges := make([]signedInEdge, 0, len(origins))
	for _, origin := range origins {
		entry := signedInEdge{Edge: origin}
		session, err := sessionOn(dir, origin)
		if err != nil {
			entry.Error = err.Error()
		} else {
			entry.SigninOrigin, entry.Account, entry.Device = session.SigninOrigin, &session.Account, &session.Device
		}
		edges = append(edges, entry)
	}
	return struct {
		Edges []signedInEdge `json:"edges"`
		Login *loginView     `json:"login,omitempty"`
	}{Edges: edges, Login: g.local.edge.view()}, nil
}

func sessionOn(dir, origin string) (edgeclient.Session, error) {
	client, err := edgeclient.New(dir, origin)
	if err != nil {
		return edgeclient.Session{}, err
	}
	return client.Session()
}

func (g *Gateway) localEdgeLogout(r *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		Edge string `json:"edge"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	client, perr := chooseEdge(params.Edge)
	if perr != nil {
		return nil, perr
	}
	g.local.edge.forget(client.URL())
	if err := client.Logout(r.Context()); err != nil {
		return nil, edgeError(err)
	}
	return struct {
		Edge string `json:"edge"`
	}{Edge: client.URL()}, nil
}

func (g *Gateway) localEdgeServers(r *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		Edge string `json:"edge"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	client, perr := chooseEdge(params.Edge)
	if perr != nil {
		return nil, perr
	}
	servers, err := client.Servers(r.Context())
	if err != nil {
		return nil, edgeError(err)
	}
	if servers == nil {
		servers = []edgeproto.ServerInfo{}
	}
	return struct {
		Edge    string                 `json:"edge"`
		Servers []edgeproto.ServerInfo `json:"servers"`
	}{Edge: client.URL(), Servers: servers}, nil
}

// edgeLinkParams are the params edge.link and edge.claim share: the
// edge, an SSH address to try first, and the profile name.
type edgeLinkParams struct {
	Edge string `json:"edge"`
	Addr string `json:"addr"`
	Name string `json:"name"`
}

// edgeLinked is the answer of edge.link and edge.claim.
type edgeLinked struct {
	ServerID string          `json:"server_id"`
	Edge     string          `json:"edge"`
	Addr     string          `json:"addr,omitempty"`
	User     string          `json:"user"`
	Member   protocol.Member `json:"member"`
}

func (g *Gateway) localEdgeLink(_ *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		edgeLinkParams
		ServerID string `json:"server_id"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	if !edgeproto.ValidServerID(params.ServerID) {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: fmt.Sprintf("server_id %q is not a server id; edge.servers lists them", params.ServerID)}
	}
	client, perr := chooseEdge(params.Edge)
	if perr != nil {
		return nil, perr
	}
	return g.linkEdge(cli.LinkOptions{Addr: params.Addr, Name: params.Name, EdgeURL: client.URL(), ServerID: params.ServerID})
}

// localEdgeHostKey reads the host key fingerprint of a server through
// the edge, as aether link --from-edge shows it before asking, without
// authenticating to the server.
func (g *Gateway) localEdgeHostKey(r *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		Edge     string `json:"edge"`
		ServerID string `json:"server_id"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	if !edgeproto.ValidServerID(params.ServerID) {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: fmt.Sprintf("server_id %q is not a server id; edge.servers lists them", params.ServerID)}
	}
	client, perr := chooseEdge(params.Edge)
	if perr != nil {
		return nil, perr
	}
	fingerprint, err := cli.HostKeyFingerprint(r.Context(), client.URL(), params.ServerID)
	if err != nil {
		return nil, edgeError(err)
	}
	return struct {
		Edge        string `json:"edge"`
		ServerID    string `json:"server_id"`
		Fingerprint string `json:"fingerprint"`
	}{Edge: client.URL(), ServerID: params.ServerID, Fingerprint: fingerprint}, nil
}

// localEdgeClaim claims a server with the code aether-server setup
// printed and links it, as aether link --claim does.
func (g *Gateway) localEdgeClaim(_ *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		edgeLinkParams
		Code string `json:"code"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	if _, _, err := edgeproto.ParseClaimCode(params.Code); err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()}
	}
	client, perr := chooseEdge(params.Edge)
	if perr != nil {
		return nil, perr
	}
	return g.linkEdge(cli.LinkOptions{Addr: params.Addr, Name: params.Name, EdgeURL: client.URL(), Claim: params.Code})
}

// linkEdge links the gateway to a server through an edge and moves every
// later request onto it, as link.apply does for an address.
func (g *Gateway) linkEdge(opts cli.LinkOptions) (*edgeLinked, *protocol.Error) {
	result, err := cli.Link(opts, g.local.snapshot())
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: err.Error()}
	}
	if perr := g.adoptLink(result); perr != nil {
		return nil, perr
	}
	cfg := result.Config
	return &edgeLinked{ServerID: cfg.ServerID, Edge: cfg.EdgeURL, Addr: cfg.Addr, User: cfg.User, Member: result.Info.Member}, nil
}
