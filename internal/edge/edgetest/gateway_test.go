package edgetest

import (
	"encoding/json"
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

	var login struct {
		State        string `json:"state"`
		UserCode     string `json:"user_code"`
		SigninOrigin string `json:"signin_origin"`
	}
	h.advance(5 * time.Minute)
	gatewayCall(t, g, "/local/v1/edge.login", `{"edge":"`+h.relayURL+`","label":"alice-desktop"}`, &login)
	if login.SigninOrigin != h.signinURL {
		t.Fatalf("edge.login names sign-in origin %q, want %q", login.SigninOrigin, h.signinURL)
	}
	b, err := h.signIn(alice)
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
