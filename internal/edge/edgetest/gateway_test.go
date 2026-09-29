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
)

// gatewayCall posts one request to the local gateway with its token.
func gatewayCall(t *testing.T, g *localgw.Gateway, path, body string, out any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.Token())
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d: %s", path, rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("%s: %v: %s", path, err, rec.Body)
	}
}

// The desktop onboarding wizard signs in, claims and links a server
// through the local gateway alone, then reaches it over that link.
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
}
