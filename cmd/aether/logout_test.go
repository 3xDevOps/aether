package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/cli"
	edgeclient "github.com/3xDevOps/Aether/internal/edge/client"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/testhome"
)

func TestDeleteEdgeAccountShowsWhatItTouchesAndWhere(t *testing.T) {
	testhome.Isolate(t)
	token := edgeproto.NewToken()
	owned := edgeproto.ServerInfo{ID: testServerID, Name: "prod", Role: "admin"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+edgeproto.PathAccount, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(edgeproto.AccountSummary{
			Account: edgeproto.AccountInfo{ID: edgeproto.NewAccountID(), Account: edgeproto.Account{
				Provider: edgeproto.ProviderGitHub, Subject: "1001", Login: "octo"}},
			Owned: []edgeproto.ServerInfo{owned}, Member: []edgeproto.ServerInfo{}, Confirm: "octo",
		})
	})
	edge := httptest.NewServer(mux)
	t.Cleanup(edge.Close)
	dir, err := cli.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"edges": map[string]any{edge.URL: map[string]any{"token": token, "signin_origin": edge.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, edgeclient.TokensFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := edgeclient.New(dir, edge.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	var out bytes.Buffer
	if err := deleteEdgeAccount(ctx, client, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"octo (github)", testServerID, "aether member transfer <member id>",
		"servers it is a member of: none", "keeps\nworking there until an admin removes it",
		"delete it in a browser at " + edge.URL + "/account: sign in with GitHub there and type octo"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if _, err := client.Session(); err != nil {
		t.Fatalf("token after listing what a deletion touches: %v, want it kept", err)
	}
}
