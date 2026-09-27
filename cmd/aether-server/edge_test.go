package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/edgeagent"
	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestEdgeURLOptionIsValidated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.conf")
	var out bytes.Buffer
	if err := configSet(&out, path, "edge-url", "http://edge.example.com"); err == nil {
		t.Error("config accepted a plain-http edge url")
	}
	for _, ok := range []string{"", "https://edge.example.com"} {
		if err := configSet(&out, path, "edge-url", ok); err != nil {
			t.Errorf("config set edge-url %q: %v", ok, err)
		}
	}
}

func TestEdgeStatusAndClaimCode(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := edgeClaimCode(&out, dir, edgeagent.DefaultURL); err != nil {
		t.Fatal(err)
	}
	id, err := serverID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "aether link --claim "+id[:edgeproto.ClaimPrefixLength]+"-") {
		t.Errorf("claim-code output lacks the link command:\n%s", out.String())
	}

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := edgeagent.OpenState(dir).Pin(pub); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := edgeStatus(&out, dir, edgeagent.DefaultURL, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{id, edgeproto.EdgeKeyFingerprint(pub), "5 attempts left", "never connected"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, out.String())
		}
	}
}

func TestEdgeTrustNeedsConfirmation(t *testing.T) {
	offered, _, _ := ed25519.GenerateKey(rand.Reader)
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(edgeproto.EdgeKeyResponse{Key: offered})
	}))
	defer edge.Close()
	dir := t.TempDir()
	old, _, _ := ed25519.GenerateKey(rand.Reader)
	state := edgeagent.OpenState(dir)
	if err := state.Pin(old); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := edgeTrust(&out, strings.NewReader("y\n"), dir, edge.URL); err == nil {
		t.Fatal("trust pinned without a typed yes")
	}
	if pinned, _ := state.PinnedKey(); !pinned.Equal(old) {
		t.Fatal("pin changed without confirmation")
	}
	for _, want := range []string{edgeproto.EdgeKeyFingerprint(old), edgeproto.EdgeKeyFingerprint(offered)} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("trust does not show %s:\n%s", want, out.String())
		}
	}
	if err := edgeTrust(&out, strings.NewReader("yes\n"), dir, edge.URL); err != nil {
		t.Fatal(err)
	}
	if pinned, _ := state.PinnedKey(); !pinned.Equal(offered) {
		t.Fatal("confirmed trust did not pin the offered key")
	}
}

type fakeApprover struct {
	dev      *domain.Device
	approved domain.DeviceID
}

func (f *fakeApprover) GetDeviceByApprovalCode(_ context.Context, code string) (*domain.Device, error) {
	if f.dev == nil || code != f.dev.ApprovalCode {
		return nil, store.ErrNotFound
	}
	return f.dev, nil
}

func (f *fakeApprover) ApproveDevice(_ context.Context, id domain.DeviceID, _ domain.MemberID) error {
	f.approved = id
	return nil
}

func (f *fakeApprover) GetMember(_ context.Context, id domain.MemberID) (*domain.Member, error) {
	return &domain.Member{ID: id, DisplayName: "Octo"}, nil
}

func TestDeviceApprove(t *testing.T) {
	db := &fakeApprover{dev: &domain.Device{ID: "d1", Member: "m1", Label: "laptop", ApprovalCode: "ABCD-EFGH"}}
	var out bytes.Buffer
	if err := deviceApprove(context.Background(), &out, db, "WRONG"); err == nil || !strings.Contains(err.Error(), "no pending device") {
		t.Fatalf("wrong code: %v", err)
	}
	if err := deviceApprove(context.Background(), &out, db, "ABCD-EFGH"); err != nil {
		t.Fatal(err)
	}
	if db.approved != "d1" || !strings.Contains(out.String(), `"laptop" of Octo`) {
		t.Fatalf("approved %q, output %q", db.approved, out.String())
	}
}
