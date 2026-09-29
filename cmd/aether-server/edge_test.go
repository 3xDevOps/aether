package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeagent "github.com/3xDevOps/Aether/internal/edge/agent"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/serversetup"
	"github.com/3xDevOps/Aether/internal/store"
	"golang.org/x/crypto/ssh"
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
	if err := edgeClaimCode(&out, dir, ""); err != nil {
		t.Fatal(err)
	}
	id, err := serverID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "aether link --claim "+id+"-") {
		t.Errorf("claim-code output lacks the link command:\n%s", out.String())
	}

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := edgeagent.OpenState(dir).Pin(pub); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := edgeStatus(&out, dir, edgeagent.DefaultURL, edgeproto.PolicyApprovedDevices, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{id, edgeproto.EdgeKeyFingerprint(pub), "5 attempts left", "never connected", "edge access  approved-devices"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, out.String())
		}
	}
}

// claim-code --admin names the existing admin a console recovery binds
// the claiming account to. A member who is not an admin, or none, is
// refused before a code exists.
func TestClaimCodeForAnAdmin(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	admin := &domain.Member{DisplayName: "Ada", PublicKey: deviceKey(t), Color: "#e6194b", Role: domain.RoleAdmin}
	viewer := &domain.Member{DisplayName: "Vic", PublicKey: deviceKey(t), Color: "#3cb44b", Role: domain.RoleViewer}
	for _, m := range []*domain.Member{admin, viewer} {
		if err := db.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	state := edgeagent.OpenState(dir)
	var out bytes.Buffer
	for id, want := range map[domain.MemberID]string{viewer.ID: "is viewer; console recovery restores an admin", "nobody": "no member nobody; the admins are: " + string(admin.ID) + " (Ada)"} {
		if err := edgeClaimCode(&out, dir, id); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("claim-code --admin %s = %v, want %q", id, err, want)
		}
		if _, ok, _ := state.ClaimCode(); ok {
			t.Fatalf("claim-code --admin %s issued a code", id)
		}
	}
	if err := edgeClaimCode(&out, dir, admin.ID); err != nil {
		t.Fatal(err)
	}
	if c, ok, err := state.ClaimCode(); err != nil || !ok || c.Admin != string(admin.ID) {
		t.Fatalf("claim code = %+v, %v, %v; want one naming %s", c, ok, err, admin.ID)
	}
	if !strings.Contains(out.String(), "binds the claiming account to admin Ada ("+string(admin.ID)+")") {
		t.Fatalf("claim-code --admin does not say what the claim does:\n%s", out.String())
	}
}

// An upgraded server whose config never named edge-url must not start
// dialing an edge: only setup, or an explicit flag or config key, turns it
// on.
func TestEdgeIsOffUnlessConfigured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.conf")
	if err := serversetup.WriteConfig(path, map[string]string{"addr": ":2300"}); err != nil {
		t.Fatal(err)
	}
	o, _, _, err := loadOptions("serve", []string{"--config", path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if *o.edgeURL != "" {
		t.Fatalf("edge-url = %q from a config without it, want empty", *o.edgeURL)
	}
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	serveFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if v, ok := installValues(fs)["edge-url"]; ok {
		t.Fatalf("install without --edge-url wrote edge-url = %q", v)
	}
	fs = flag.NewFlagSet("install", flag.ContinueOnError)
	serveFlags(fs)
	if err := fs.Parse([]string{"--edge-url", edgeagent.DefaultURL}); err != nil {
		t.Fatal(err)
	}
	if v := installValues(fs)["edge-url"]; v != edgeagent.DefaultURL {
		t.Fatalf("install --edge-url wrote %q, want %s", v, edgeagent.DefaultURL)
	}
	if *o.edgeAccess != accessPolicyValue(edgeproto.PolicyApprovedDevices) {
		t.Fatalf("edge-access = %q from a config without it, want approved-devices", *o.edgeAccess)
	}
}

// Turning the edge on at install names a policy; nothing defaults it.
func TestInstallWithAnEdgeNeedsAnAccessPolicy(t *testing.T) {
	for args, want := range map[string]string{
		"--edge-url " + edgeagent.DefaultURL:                             "needs --edge-access",
		"--edge-url " + edgeagent.DefaultURL + " --edge-access account":  "",
		"--edge-url= --addr :2300":                                       "",
		"--addr :2300":                                                   "",
		"--edge-url " + edgeagent.DefaultURL + " --edge-access=":         "name a policy",
		"--edge-url " + edgeagent.DefaultURL + " --edge-access everyone": "unknown access policy",
	} {
		fs := flag.NewFlagSet("install", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		serveFlags(fs)
		err := fs.Parse(strings.Fields(args))
		if err == nil {
			err = requireEdgeAccess(installValues(fs))
		}
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("install %s: %v, want %q", args, err, want)
		}
	}
}

// The policy changes only here, with its previous value shown; an empty
// value is refused rather than read as a policy.
func TestConfigSetEdgeAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.conf")
	var out bytes.Buffer
	if err := configSet(&out, path, "edge-access", ""); err == nil {
		t.Fatal("config set accepted an empty edge-access")
	}
	if err := configSet(&out, path, "edge-access", "account"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "edge-access = account (was unset, meaning approved-devices)") {
		t.Errorf("first change does not show the previous policy:\n%s", out.String())
	}
	out.Reset()
	if err := configSet(&out, path, "edge-access", "approved-devices"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "edge-access = approved-devices (was account)") {
		t.Errorf("second change does not show the previous policy:\n%s", out.String())
	}
	if _, err := serversetup.Apply(serveFlagSet(), map[string]string{"edge-device-approval": "false"}); err == nil {
		t.Error("the replaced edge-device-approval option is still accepted")
	}
}

func TestEdgeTrustNeedsConfirmation(t *testing.T) {
	offered, _, _ := ed25519.GenerateKey(rand.Reader)
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(edgeproto.EdgeInfo{SigninOrigin: "https://auth.example.test", Key: offered,
			Fingerprint: edgeproto.EdgeKeyFingerprint(offered), Version: edgeproto.Version, MinVersion: edgeproto.MinVersion})
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

// newDeviceStore returns a store with octo's github identity bound to a
// member, and that member's devices of the given statuses.
func newDeviceStore(t *testing.T, statuses ...domain.DeviceStatus) (*store.DB, []*domain.Device) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	admin := &domain.Member{DisplayName: "Ada", PublicKey: deviceKey(t), Color: "#e6194b", Role: domain.RoleAdmin}
	if err = db.CreateMember(ctx, admin); err != nil {
		t.Fatal(err)
	}
	inv := &domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleCollaborator, CreatedBy: admin.ID,
		ExpiresAt: time.Now().Add(time.Hour)}
	if err = db.CreateInvitation(ctx, inv); err != nil {
		t.Fatal(err)
	}
	m, err := db.AcceptInvitation(ctx, inv.ID, &domain.Identity{Provider: "github", Subject: "1001", Login: "octo"},
		&domain.Member{DisplayName: "Octo", Color: "#3cb44b"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var devs []*domain.Device
	for i, status := range statuses {
		dev := &domain.Device{Member: m.ID, Provider: "github", Subject: "1001", Login: "octo", Credential: deviceKey(t),
			Label: fmt.Sprintf("device-%d", i), Status: status}
		if err := db.RegisterDevice(ctx, dev); err != nil {
			t.Fatal(err)
		}
		devs = append(devs, dev)
	}
	return db, devs
}

func deviceKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return edgeproto.DeviceKeyLine(key)
}

func TestDeviceApprove(t *testing.T) {
	db, devs := newDeviceStore(t, domain.DevicePending, domain.DeviceRegistered)
	var out bytes.Buffer
	if err := deviceApprove(context.Background(), &out, strings.NewReader("y\n"), db, "WRONG"); err == nil || !strings.Contains(err.Error(), "no device is waiting") {
		t.Fatalf("wrong code: %v", err)
	}
	// Enter, or the end of input, approves nothing.
	for _, answer := range []string{"\n", ""} {
		out.Reset()
		if err := deviceApprove(context.Background(), &out, strings.NewReader(answer), db, devs[0].ApprovalCode); err == nil || !strings.Contains(err.Error(), "not approved") {
			t.Fatalf("answer %q: %v", answer, err)
		}
		if got, _ := db.GetDevice(context.Background(), devs[0].ID); got.Status != domain.DevicePending {
			t.Fatalf("device after answer %q = %s", answer, got.Status)
		}
		if !strings.Contains(out.String(), `"device-0" of Octo (`) || !strings.Contains(out.String(), "), collaborator, signed in as github octo") {
			t.Fatalf("the prompt does not name the member and role approving admits:\n%s", out.String())
		}
	}
	for _, dev := range devs {
		if err := deviceApprove(context.Background(), &out, strings.NewReader("yes\n"), db, dev.ApprovalCode); err != nil {
			t.Fatal(err)
		}
		got, _ := db.GetDevice(context.Background(), dev.ID)
		if got.Status != domain.DeviceApproved || got.ApprovedBy != "" {
			t.Fatalf("device after approval on the machine = %+v", got)
		}
	}
}

// A device waiting on an invitation, approved on the machine, accepts the
// invitation: the member is created with the invited role only then.
func TestDeviceApproveAcceptsAnInvitation(t *testing.T) {
	db, _ := newDeviceStore(t)
	ctx := context.Background()
	members, _ := db.ListMembers(ctx)
	inv := &domain.Invitation{Provider: "github", Login: "dana", Role: domain.RoleViewer, CreatedBy: members[0].ID,
		ExpiresAt: time.Now().Add(time.Hour)}
	if err := db.CreateInvitation(ctx, inv); err != nil {
		t.Fatal(err)
	}
	dev := &domain.Device{Invitation: inv.ID, Provider: "github", Subject: "2002", Login: "dana", Name: "Dana",
		Credential: deviceKey(t), Label: "dana-laptop", Status: domain.DevicePending}
	if err := db.RegisterInvitationDevice(ctx, dev, time.Now()); err != nil {
		t.Fatal(err)
	}
	var review bytes.Buffer
	if err := deviceReview(ctx, &review, strings.NewReader("\n\n"), db, edgeproto.PolicyApprovedDevices); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(review.String(), `"dana-laptop" waiting on invitation `+string(inv.ID)+" (approving adds a new member, viewer), signed in as github dana") {
		t.Fatalf("review does not say what approving the invitation device does:\n%s", review.String())
	}
	var out bytes.Buffer
	if err := deviceApprove(ctx, &out, strings.NewReader("y\n"), db, dev.ApprovalCode); err != nil {
		t.Fatal(err)
	}
	m, err := db.GetMemberByIdentity(ctx, "github", "2002")
	if err != nil || m.Role != domain.RoleViewer || m.DisplayName != "Dana" {
		t.Fatalf("member after approval on the machine = %+v, %v", m, err)
	}
	if !strings.Contains(out.String(), "invitation "+string(inv.ID)+" accepted: Dana joined as viewer") {
		t.Fatalf("output %q", out.String())
	}
}

// The review lists every device awaiting approval with its member and
// times, acts only on typed answers, and lists the credentials the edge
// policy does not cover.
func TestDeviceReview(t *testing.T) {
	db, devs := newDeviceStore(t, domain.DeviceRegistered, domain.DevicePending, domain.DevicePending, domain.DeviceApproved)
	var out bytes.Buffer
	if err := deviceReview(context.Background(), &out, strings.NewReader("a\nmaybe\nr\n\n"), db, edgeproto.PolicyApprovedDevices); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i, want := range []domain.DeviceStatus{domain.DeviceApproved, domain.DeviceRevoked, domain.DevicePending, domain.DeviceApproved} {
		if got, _ := db.GetDevice(ctx, devs[i].ID); got.Status != want {
			t.Errorf("device %d = %s, want %s", i, got.Status, want)
		}
	}
	for _, want := range []string{`"device-0" of Octo`, "signed in as github octo", "registered, key SHA256:", "first seen", "last seen never",
		"SSH key SHA256:", "of Ada", "admin", "aether member remove"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("review does not say %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "device-3") {
		t.Errorf("review lists an approved device:\n%s", out.String())
	}
	for _, dev := range devs[:3] {
		if strings.Contains(out.String(), dev.ApprovalCode) {
			t.Errorf("review shows approval code %s", dev.ApprovalCode)
		}
	}
}
