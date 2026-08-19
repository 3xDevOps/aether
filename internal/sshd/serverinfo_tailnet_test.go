package sshd

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// server.info reports the tailnet hostname discovered at startup and
// whether WhoIs identity auth is active; both stay off the wire when the
// server is not on a tailnet.
func TestServerInfoTailnetFields(t *testing.T) {
	e := newTestEnv(t, func(c *Config) {
		c.WhoIs = &fakeWhoIs{err: errors.New("not on a tailnet")}
		c.TailnetHostname = "box.tail1234.ts.net"
	})
	c := controlClient(t, e)

	var info protocol.ServerInfoResult
	if err := c.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil {
		t.Fatalf("server.info: %v", err)
	}
	if info.TailnetHostname != "box.tail1234.ts.net" {
		t.Errorf("tailnet_hostname = %q, want box.tail1234.ts.net", info.TailnetHostname)
	}
	if !info.TailnetIdentityAuth {
		t.Error("tailnet_identity_auth = false, want true when WhoIs is configured")
	}

	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tailnet_hostname", "tailnet_identity_auth"} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("server.info JSON missing %q: %s", key, raw)
		}
	}
}

// Off-tailnet servers omit both fields from the wire entirely.
func TestServerInfoTailnetFieldsOmitted(t *testing.T) {
	e := newTestEnv(t, nil)
	c := controlClient(t, e)

	var info protocol.ServerInfoResult
	if err := c.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil {
		t.Fatalf("server.info: %v", err)
	}
	if info.TailnetHostname != "" || info.TailnetIdentityAuth {
		t.Errorf("off-tailnet info = %+v, want empty tailnet fields", info)
	}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tailnet") {
		t.Errorf("off-tailnet server.info leaks tailnet keys: %s", raw)
	}
}
