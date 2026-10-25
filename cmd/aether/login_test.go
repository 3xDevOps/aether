package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/cli"
	edgeclient "github.com/3xDevOps/Aether/internal/edge/client"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/testhome"
)

func TestLoginJSONPrintsTheCodeFirstAndTheAccountLast(t *testing.T) {
	testhome.Isolate(t)
	edgeKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	token := edgeproto.NewToken()
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set(edgeproto.HeaderVersion, "1")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	var (
		edge      *httptest.Server
		deviceKey = make(chan string, 1)
	)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+edgeproto.PathEdgeInfo, func(w http.ResponseWriter, r *http.Request) {
		reply(w, edgeproto.EdgeInfo{SigninOrigin: edge.URL, Key: edgeKey, Fingerprint: edgeproto.EdgeKeyFingerprint(edgeKey),
			Version: edgeproto.Version, MinVersion: edgeproto.MinVersion})
	})
	mux.HandleFunc("POST "+edgeproto.PathDeviceStart, func(w http.ResponseWriter, r *http.Request) {
		var req edgeproto.DeviceStartRequest
		if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
			t.Error(decodeErr)
		}
		deviceKey <- req.Key
		reply(w, edgeproto.DeviceStartResponse{DeviceCode: "device-code", UserCode: "ABCD-EFGH",
			VerificationURI: edge.URL + "/device", ExpiresIn: 600, Interval: 1})
	})
	mux.HandleFunc("POST "+edgeproto.PathDeviceToken, func(w http.ResponseWriter, r *http.Request) {
		reply(w, edgeproto.DeviceTokenResponse{
			Token:  token,
			Device: edgeproto.Device{ID: "dev-1", Label: "laptop", Key: <-deviceKey},
			Account: edgeproto.AccountInfo{ID: edgeproto.NewAccountID(), Account: edgeproto.Account{
				Provider: edgeproto.ProviderGitHub, Subject: "42", Login: "octo"}},
		})
	})
	edge = httptest.NewServer(mux)
	t.Cleanup(edge.Close)
	dir, err := cli.Dir()
	if err != nil {
		t.Fatal(err)
	}
	client, err := edgeclient.New(dir, edge.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	login, err := client.StartLogin(ctx, "laptop")
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err = loginJSON(ctx, &out, client, login); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), token) {
		t.Fatalf("output carries the device token:\n%s", out.String())
	}
	raw := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(raw) != 2 {
		t.Fatalf("output = %q, want two JSON lines", out.String())
	}
	var first, last loginLine
	if err = json.Unmarshal([]byte(raw[0]), &first); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(raw[1]), &last); err != nil {
		t.Fatal(err)
	}
	expires, err := time.Parse(time.RFC3339, first.ExpiresAt)
	if first.State != "pending" || first.UserCode != "ABCD-EFGH" || first.VerificationURI != edge.URL+"/device" ||
		err != nil || time.Until(expires) < 9*time.Minute {
		t.Errorf("first line = %s, want the pending code, its address and an expiry ten minutes out", raw[0])
	}
	if last.State != "signed_in" || last.Account == nil || last.Account.Login != "octo" || last.Device == nil || last.Device.ID != "dev-1" {
		t.Errorf("last line = %s, want signed_in as octo on dev-1", raw[1])
	}
}
