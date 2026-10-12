package main

import (
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestEdgeConnectArgs(t *testing.T) {
	for _, args := range [][]string{nil, {"status"}, {"connect", "extra"}} {
		if err := runEdge(args); err == nil || err.Error() != edgeUsage {
			t.Errorf("%q: err = %v, want the usage", args, err)
		}
	}
}

func TestEdgeConnected(t *testing.T) {
	for _, tc := range []struct {
		edge    protocol.ServerEdgeResult
		msg     string
		wantErr string
	}{
		{edge: protocol.ServerEdgeResult{}, wantErr: "the server's edge is off: edge-url is empty on the server"},
		{edge: protocol.ServerEdgeResult{EdgeURL: "https://edge.example"}},
		{
			edge: protocol.ServerEdgeResult{EdgeURL: "https://edge.example", Owner: &protocol.EdgeAccount{Provider: "github", Login: "octo"}},
			msg:  "this server is already connected to https://edge.example as github octo\n",
		},
	} {
		msg, err := edgeConnected(tc.edge)
		if msg != tc.msg || (err == nil) != (tc.wantErr == "") || (err != nil && err.Error() != tc.wantErr) {
			t.Errorf("%+v = %q, %v; want %q, %q", tc.edge, msg, err, tc.msg, tc.wantErr)
		}
	}
}
