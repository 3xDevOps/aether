package edgetest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/cli"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/localgw"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/testhome"
	"github.com/3xDevOps/Aether/internal/webgate"
)

// gatewayPost posts one request to the local gateway with its token.
func gatewayPost(g *localgw.Gateway, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.Token())
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	return rec
}

// gatewayCall posts one request to the local gateway and decodes its
// answer into out.
func gatewayCall(t *testing.T, g *localgw.Gateway, path, body string, out any) {
	t.Helper()
	rec := gatewayPost(g, path, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d: %s", path, rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("%s: %v: %s", path, err, rec.Body)
	}
}

// gatewaySignIn signs the gateway in to the edge as user, as the
// dashboard's sign-in step does, confirming the code in user's browser.
func gatewaySignIn(t *testing.T, h *harness, g *localgw.Gateway, user ghUser, label string) {
	t.Helper()
	var login struct {
		State        string `json:"state"`
		UserCode     string `json:"user_code"`
		SigninOrigin string `json:"signin_origin"`
	}
	h.advance(5 * time.Minute)
	gatewayCall(t, g, "/local/v1/edge.login", `{"edge":"`+h.relayURL+`","label":"`+label+`"}`, &login)
	if login.SigninOrigin != h.signinURL {
		t.Fatalf("edge.login names sign-in origin %q, want %q", login.SigninOrigin, h.signinURL)
	}
	b, err := h.signIn(user)
	if err == nil {
		err = b.approveDevice(login.UserCode)
	}
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "gateway signed in", func() error {
		var st struct {
			Login struct {
				State string `json:"state"`
				Error string `json:"error"`
			} `json:"login"`
		}
		gatewayCall(t, g, "/local/v1/edge.status", "{}", &st)
		if st.Login.State != "signed_in" {
			return fmt.Errorf("sign-in %s %s", st.Login.State, st.Login.Error)
		}
		return nil
	})
}

// The desktop onboarding wizard signs in, claims and links a server
// through the local gateway alone, then reaches it over that link. It
// then joins another server by an invitation's link, calling edge.join
// again while the device waits, as the dashboard does, until that
// server's admin approves the device by its id.
func TestGatewaySignsInClaimsAndLinks(t *testing.T) {
	h := newHarness(t)
	s := h.newServer(edgeproto.PolicyApprovedDevices)
	testhome.Isolate(t)
	g, err := localgw.New(localgw.Config{Backend: localgw.NewSSHBackend(cli.Config{})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })

	gatewaySignIn(t, h, g, alice, "alice-desktop")

	h.advance(time.Minute)
	var linked struct {
		ServerID string          `json:"server_id"`
		Edge     string          `json:"edge"`
		Member   protocol.Member `json:"member"`
	}
	gatewayCall(t, g, "/local/v1/edge.claim", `{"code":"`+s.claimCode(t, time.Now())+`"}`, &linked)
	if linked.ServerID != s.id || linked.Edge != h.relayURL || linked.Member.Role != "admin" {
		t.Fatalf("edge.claim = %+v", linked)
	}

	var status struct {
		ServerID string `json:"server_id"`
		EdgeURL  string `json:"edge_url"`
	}
	gatewayCall(t, g, "/local/v1/link.status", "{}", &status)
	if status.ServerID != s.id || status.EdgeURL != h.relayURL {
		t.Fatalf("link.status = %+v", status)
	}
	var devices protocol.MemberDeviceListResult
	gatewayCall(t, g, "/api/v1/"+protocol.MethodMemberDeviceList, "{}", &devices)
	if len(devices.Devices) != 1 || devices.Devices[0].Label != "alice-desktop" || devices.Devices[0].MemberID != linked.Member.ID ||
		devices.Devices[0].Status != "approved" {
		t.Fatalf("member.device.list through the gateway = %+v", devices.Devices)
	}

	other := h.newServer(edgeproto.PolicyApprovedDevices)
	bo := h.login(bob)[0]
	h.claimServer(bo, other)
	bctl := h.control(bo, other)
	join := `{"link":"` + invite(t, bctl, protocol.MemberInvitationCreateParams{Login: alice.Login, Role: "collaborator"}).Link + `"}`
	eventually(t, "the join to wait for approval", func() error {
		rec := gatewayPost(g, "/local/v1/edge.join", join)
		var body webgate.ErrorBody
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body.Error == nil || body.Error.Code != protocol.CodeInvalidState {
			return fmt.Errorf("edge.join = %d: %s", rec.Code, rec.Body)
		}
		var data struct {
			Refusal edgeproto.Refused `json:"refusal"`
		}
		if err := json.Unmarshal(body.Error.Data, &data); err != nil || data.Refusal.Kind != edgeproto.RefusedDevicePending {
			return fmt.Errorf("edge.join refused with %s", body.Error.Data)
		}
		return nil
	})
	var waiting protocol.MemberDeviceListResult
	if err := bctl.Call(protocol.MethodMemberDeviceList, struct{}{}, &waiting); err != nil {
		t.Fatal(err)
	}
	var deviceID string
	for _, d := range waiting.Devices {
		if d.Label == "alice-desktop" && d.Status == "pending" {
			deviceID = d.ID
		}
	}
	call[protocol.MemberDeviceResult](t, bctl, protocol.MethodMemberDeviceApprove, protocol.MemberDeviceApproveParams{DeviceID: deviceID})
	gatewayCall(t, g, "/local/v1/edge.join", join, &linked)
	if linked.ServerID != other.id || linked.Edge != h.relayURL || linked.Member.Role != "collaborator" {
		t.Fatalf("edge.join after approval = %+v", linked)
	}
}

// gatewayFailure is the error data a gateway request failed with.
type gatewayFailure struct {
	Code     int
	Message  string
	Refusal  *edgeproto.Refused
	EdgePath *struct {
		Layer    string                 `json:"layer"`
		ServerID string                 `json:"server_id"`
		Edge     string                 `json:"edge"`
		Account  string                 `json:"account"`
		Online   []edgeproto.ServerInfo `json:"online"`
	}
}

// serverInfo calls server.info through the gateway and returns its
// failure, nil when it succeeded.
func serverInfo(t *testing.T, g *localgw.Gateway) *gatewayFailure {
	t.Helper()
	rec := gatewayPost(g, "/api/v1/"+protocol.MethodServerInfo, "{}")
	if rec.Code == http.StatusOK {
		return nil
	}
	var body webgate.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == nil {
		t.Fatalf("server.info = %d: %s", rec.Code, rec.Body)
	}
	f := &gatewayFailure{Code: rec.Code, Message: body.Error.Message}
	if len(body.Error.Data) > 0 {
		var data struct {
			Refusal  *edgeproto.Refused `json:"refusal"`
			EdgePath json.RawMessage    `json:"edge_path"`
		}
		if err := json.Unmarshal(body.Error.Data, &data); err != nil {
			t.Fatalf("server.info error data %s: %v", body.Error.Data, err)
		}
		f.Refusal = data.Refusal
		if len(data.EdgePath) > 0 {
			f.EdgePath = &struct {
				Layer    string                 `json:"layer"`
				ServerID string                 `json:"server_id"`
				Edge     string                 `json:"edge"`
				Account  string                 `json:"account"`
				Online   []edgeproto.ServerInfo `json:"online"`
			}{}
			if err := json.Unmarshal(data.EdgePath, f.EdgePath); err != nil {
				t.Fatal(err)
			}
		}
	}
	return f
}

// failsAt waits until server.info through g fails at layer on the way to
// serverID, with refusal kind when it is set, and returns that failure.
func failsAt(t *testing.T, g *localgw.Gateway, layer, serverID, edgeHost string, kind edgeproto.RefusedKind) *gatewayFailure {
	t.Helper()
	var last *gatewayFailure
	eventually(t, "server.info to fail at "+layer, func() error {
		last = serverInfo(t, g)
		switch {
		case last == nil:
			return errors.New("server.info succeeded")
		case last.Code != http.StatusServiceUnavailable || last.EdgePath == nil:
			return fmt.Errorf("server.info = %d %q without edge_path", last.Code, last.Message)
		case last.EdgePath.Layer != layer || last.EdgePath.ServerID != serverID || last.EdgePath.Edge != edgeHost:
			return fmt.Errorf("server.info failed with edge_path %+v: %s", *last.EdgePath, last.Message)
		case kind != "" && (last.Refusal == nil || last.Refusal.Kind != kind):
			return fmt.Errorf("server.info refused with %+v, want kind %s: %s", last.Refusal, kind, last.Message)
		}
		return nil
	})
	return last
}

func reachable(t *testing.T, g *localgw.Gateway, what string) {
	t.Helper()
	eventually(t, what, func() error {
		if f := serverInfo(t, g); f != nil {
			return fmt.Errorf("server.info = %d: %s", f.Code, f.Message)
		}
		return nil
	})
}

// Every failure the dashboard sees through the gateway names the layer of
// the edge path that failed, the server it dialed and the edge, and the
// gateway reaches the server again as soon as that layer recovers: after
// a server restart, an edge restart and a sign-in, and onto the server a
// link made in a terminal names, while the gateway runs.
func TestGatewayNamesTheFailedLayerAndRecovers(t *testing.T) {
	h := newHarness(t)
	s := h.newServer(edgeproto.PolicyAccount)
	testhome.Isolate(t)
	g, err := localgw.New(localgw.Config{Backend: localgw.NewSSHBackend(cli.Config{})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	gatewaySignIn(t, h, g, alice, "alice-desktop")
	h.advance(time.Minute)
	var linked struct {
		ServerID string `json:"server_id"`
	}
	gatewayCall(t, g, "/local/v1/edge.claim", `{"code":"`+s.claimCode(t, time.Now())+`"}`, &linked)
	edgeHost := strings.TrimPrefix(h.relayURL, "http://")
	reachable(t, g, "the claimed server")

	s.stop()
	f := failsAt(t, g, cli.LayerRegistration, s.id, edgeHost, edgeproto.RefusedNotConnected)
	if f.EdgePath.Account != "github account "+alice.Login || len(f.EdgePath.Online) != 0 {
		t.Fatalf("stopped server: edge_path %+v", *f.EdgePath)
	}
	s.start()
	reachable(t, g, "the restarted server")

	h.stopEdge()
	failsAt(t, g, cli.LayerClientEdge, s.id, edgeHost, "")
	h.startEdge()
	eventually(t, "server back on the restarted edge", func() error {
		if !h.relay().Online(s.id) {
			return errors.New("not online")
		}
		return nil
	})
	reachable(t, g, "the server after the edge restarted")

	// The server is reinstalled under a new id: the edge sees the new one
	// online, and the gateway's link still names the old one.
	s.stop()
	other := h.newServer(edgeproto.PolicyAccount)
	h.claimServer(h.login(alice)[0], other)
	f = failsAt(t, g, cli.LayerRegistration, s.id, edgeHost, edgeproto.RefusedNotConnected)
	if len(f.EdgePath.Online) != 1 || f.EdgePath.Online[0].ID != other.id {
		t.Fatalf("stale link: online %+v, want %s", f.EdgePath.Online, other.id)
	}
	if !strings.Contains(f.Message, "aether link --from-edge <id>") {
		t.Fatalf("stale link message %q names no relink", f.Message)
	}
	saved, err := cli.Load()
	if err != nil {
		t.Fatal(err)
	}
	saved.ServerID = other.id
	if err := cli.Save(saved); err != nil {
		t.Fatal(err)
	}
	reachable(t, g, "the server a terminal relinked to")
	var status struct {
		ServerID string `json:"server_id"`
	}
	gatewayCall(t, g, "/local/v1/link.status", "{}", &status)
	if status.ServerID != other.id {
		t.Fatalf("link.status names %s after the relink, want %s", status.ServerID, other.id)
	}

	unclaimed := h.newServer(edgeproto.PolicyAccount)
	saved.ServerID = unclaimed.id
	if err := cli.Save(saved); err != nil {
		t.Fatal(err)
	}
	failsAt(t, g, cli.LayerRegistration, unclaimed.id, edgeHost, edgeproto.RefusedUnknownServer)
	saved.ServerID = other.id
	if err := cli.Save(saved); err != nil {
		t.Fatal(err)
	}
	reachable(t, g, "the relinked server again")

	gatewayCall(t, g, "/local/v1/edge.logout", `{"edge":"`+h.relayURL+`"}`, &struct{}{})
	failsAt(t, g, cli.LayerAuthorization, other.id, edgeHost, "")
	gatewaySignIn(t, h, g, alice, "alice-desktop")
	reachable(t, g, "the server after signing in again")
}
