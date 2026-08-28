package sshd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// server.info reports the server's neutral and standard image refs so
// clients can offer the standard environment at workspace creation.
func TestServerInfoImageFields(t *testing.T) {
	e := newTestEnv(t, func(c *Config) {
		c.NeutralImage = "ghcr.io/3xdevops/aether-bootstrap:v1.2.3"
		c.StandardImage = "ghcr.io/3xdevops/aether-standard:v1.2.3"
	})
	c := controlClient(t, e)

	var info protocol.ServerInfoResult
	if err := c.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil {
		t.Fatalf("server.info: %v", err)
	}
	if info.NeutralImage != "ghcr.io/3xdevops/aether-bootstrap:v1.2.3" {
		t.Errorf("neutral_image = %q, want the configured ref", info.NeutralImage)
	}
	if info.StandardImage != "ghcr.io/3xdevops/aether-standard:v1.2.3" {
		t.Errorf("standard_image = %q, want the configured ref", info.StandardImage)
	}

	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"neutral_image", "standard_image"} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("server.info JSON missing %q: %s", key, raw)
		}
	}
}

// A server without the refs configured omits both keys from the wire, which
// is how clients detect an older server and fall back.
func TestServerInfoImageFieldsOmitted(t *testing.T) {
	e := newTestEnv(t, nil)
	c := controlClient(t, e)

	var info protocol.ServerInfoResult
	if err := c.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil {
		t.Fatalf("server.info: %v", err)
	}
	if info.NeutralImage != "" || info.StandardImage != "" {
		t.Errorf("unconfigured info = %+v, want empty image fields", info)
	}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "_image") {
		t.Errorf("unconfigured server.info leaks image keys: %s", raw)
	}
}
