package main

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeagent "github.com/3xDevOps/Aether/internal/edge/agent"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/server"
	"github.com/3xDevOps/Aether/internal/serversetup"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
	"golang.org/x/crypto/ssh"
)

const edgeUsage = "usage: aether-server edge <status|claim-code|trust|leave> [--config <file>]"

// edgeURLValue is the edge-url option: an edge URL, or empty for no edge.
// Validating it as a flag makes `config set` and the config file refuse a
// URL the agent would refuse at startup.
type edgeURLValue string

func (v *edgeURLValue) String() string { return string(*v) }

func (v *edgeURLValue) Set(s string) error {
	if s != "" {
		if _, err := edgeproto.Origin(s); err != nil {
			return err
		}
	}
	*v = edgeURLValue(s)
	return nil
}

// accessPolicyValue is the edge-access option. An empty value is refused
// here although edgeproto reads a missing policy as approved-devices: a
// person who sets the option names a policy.
type accessPolicyValue edgeproto.AccessPolicy

func (v *accessPolicyValue) String() string { return string(*v) }

func (v *accessPolicyValue) Set(s string) error {
	if s == "" {
		return fmt.Errorf("name a policy: %s or %s", edgeproto.PolicyAccount, edgeproto.PolicyApprovedDevices)
	}
	p, err := edgeproto.ParseAccessPolicy(s)
	if err != nil {
		return err
	}
	*v = accessPolicyValue(p)
	return nil
}

// loadOptions parses args as serve options over the config file, so the
// edge and device commands act on the data directory and edge the service
// uses.
func loadOptions(name string, args []string) (*serveOptions, string, []string, error) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	configPath := fs.String("config", serversetup.DefaultConfigPath, "options file to read")
	o := serveFlags(fs)
	if err := fs.Parse(args); err != nil {
		return nil, "", nil, err
	}
	if _, err := applyConfigFile(fs, *configPath, *configPath == serversetup.DefaultConfigPath); err != nil {
		return nil, "", nil, err
	}
	return o, *configPath, fs.Args(), nil
}

func edgeCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(edgeUsage)
	}
	sub := args[0]
	o, configPath, rest, err := loadOptions("edge "+sub, args[1:])
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New(edgeUsage)
	}
	edgeURL := string(*o.edgeURL)
	if edgeURL == "" && sub != "status" {
		return fmt.Errorf("the edge is off: edge-url is empty in %s", configPath)
	}
	switch sub {
	case "status":
		return edgeStatus(os.Stdout, *o.dataDir, edgeURL, edgeproto.AccessPolicy(*o.edgeAccess), time.Now())
	case "claim-code":
		return edgeClaimCode(os.Stdout, *o.dataDir, edgeURL)
	case "trust":
		return edgeTrust(os.Stdout, os.Stdin, *o.dataDir, edgeURL)
	case "leave":
		return edgeLeave(os.Stdout, *o.dataDir, edgeURL, configPath)
	default:
		return fmt.Errorf("unknown edge command %q (want status, claim-code, trust, or leave)", sub)
	}
}

func serverID(dataDir string) (string, error) {
	hostKey, err := sshd.LoadOrCreateHostKey(server.HostKeyPath(dataDir))
	if err != nil {
		return "", err
	}
	return edgeproto.ServerID(hostKey.PublicKey()), nil
}

func describeAccount(a edgeproto.Account) string {
	name := a.Login
	if name == "" {
		name = a.Email
	}
	return fmt.Sprintf("%s %s (subject %s)", a.Provider, name, a.Subject)
}

func edgeStatus(w io.Writer, dataDir, edgeURL string, policy edgeproto.AccessPolicy, now time.Time) error {
	if edgeURL == "" {
		_, _ = fmt.Fprintf(w, "edge: off\nturn it on with:\n  aether-server config set edge-url %s\n", edgeagent.DefaultURL)
		return nil
	}
	id, err := serverID(dataDir)
	if err != nil {
		return err
	}
	state, err := edgeagent.OpenState(dataDir, edgeURL)
	if err != nil {
		return err
	}
	pinned, err := state.PinnedKey()
	if err != nil {
		return err
	}
	owner, err := state.Owner()
	if err != nil {
		return err
	}
	claim, hasClaim, err := state.ClaimCode()
	if err != nil {
		return err
	}
	status, ran, err := state.Status()
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "edge\t%s\n", edgeURL)
	_, _ = fmt.Fprintf(tw, "edge access\t%s\n", policy)
	_, _ = fmt.Fprintf(tw, "server id\t%s\n", id)
	if pinned != nil {
		_, _ = fmt.Fprintf(tw, "edge key\t%s\n", edgeproto.EdgeKeyFingerprint(pinned))
	} else {
		_, _ = fmt.Fprintln(tw, "edge key\tnot pinned yet; the server pins it when it first connects")
	}
	switch {
	case owner != nil:
		_, _ = fmt.Fprintf(tw, "owner\t%s\n", describeAccount(*owner))
	case hasClaim && claim.Usable(now):
		_, _ = fmt.Fprintf(tw, "owner\tnone; claim code valid until %s, %d attempts left\n", claim.ExpiresAt.Local().Format(time.Kitchen), claim.AttemptsLeft)
	default:
		_, _ = fmt.Fprintln(tw, "owner\tnone; get a claim code with `aether-server edge claim-code`")
	}
	switch {
	case !ran:
		_, _ = fmt.Fprintln(tw, "connection\tnever connected; the server has not run with this edge")
	case status.Connected:
		_, _ = fmt.Fprintf(tw, "connection\tconnected since %s\n", status.Since.Local().Format(time.DateTime))
	default:
		_, _ = fmt.Fprintf(tw, "connection\tdisconnected since %s: %s\n", status.Since.Local().Format(time.DateTime), status.Error)
	}
	return tw.Flush()
}

func edgeClaimCode(w io.Writer, dataDir, edgeURL string) error {
	state, err := edgeagent.OpenState(dataDir, edgeURL)
	if err != nil {
		return err
	}
	owner, err := state.Owner()
	if err != nil {
		return err
	}
	if owner != nil {
		return fmt.Errorf("this server is already claimed by %s", describeAccount(*owner))
	}
	id, err := serverID(dataDir)
	if err != nil {
		return err
	}
	code, expires, err := state.IssueClaimCode(id, time.Now())
	if err != nil {
		return err
	}
	printClaimCode(w, code, expires)
	return nil
}

func printClaimCode(w io.Writer, code string, expires time.Time) {
	_, _ = fmt.Fprintf(w, "claim code: %s (valid until %s, %d attempts)\n", code, expires.Local().Format(time.Kitchen), edgeproto.ClaimCodeAttempts)
	_, _ = fmt.Fprintf(w, "claim this server with:\n  aether link --claim %s\n", code)
}

func edgeTrust(w io.Writer, in io.Reader, dataDir, edgeURL string) error {
	state, err := edgeagent.OpenState(dataDir, edgeURL)
	if err != nil {
		return err
	}
	pinned, err := state.PinnedKey()
	if err != nil {
		return err
	}
	info, err := edgeagent.FetchEdgeInfo(context.Background(), edgeURL)
	if err != nil {
		return err
	}
	key := info.Key
	offered := edgeproto.EdgeKeyFingerprint(key)
	if pinned.Equal(key) {
		_, _ = fmt.Fprintf(w, "the pinned edge key already is %s\n", offered)
		return nil
	}
	old := "none"
	if pinned != nil {
		old = edgeproto.EdgeKeyFingerprint(pinned)
	}
	_, _ = fmt.Fprintf(w, "pinned edge key:  %s\n%s presents: %s\n", old, edgeURL, offered)
	_, _ = fmt.Fprintln(w, "Compare it with the fingerprint the edge's operator publishes. A key you did not expect means another party may be answering for the edge.")
	_, _ = fmt.Fprint(w, "Type yes to pin the new key: ")
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimSpace(answer) != "yes" {
		return errors.New("not pinned; the edge key is unchanged")
	}
	if err := state.Pin(key); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "pinned %s; the server uses it when it next reconnects\n", offered)
	return nil
}

func edgeLeave(w io.Writer, dataDir, edgeURL, configPath string) error {
	hostKey, err := sshd.LoadOrCreateHostKey(server.HostKeyPath(dataDir))
	if err != nil {
		return err
	}
	agent, err := edgeagent.New(edgeagent.Config{EdgeURL: edgeURL, DataDir: dataDir, HostKey: hostKey})
	if err != nil {
		return err
	}
	if err := agent.Leave(context.Background()); err != nil {
		return fmt.Errorf("%w\nto stop using the edge without telling it, run:\n  aether-server config set edge-url \"\"", err)
	}
	_, _ = fmt.Fprintf(w, "left %s; members keep direct and tailnet access\n", edgeURL)
	return configSet(w, configPath, "edge-url", "")
}

// deviceStore is the part of the store the device commands use.
type deviceStore interface {
	GetDeviceByApprovalCode(ctx context.Context, code string) (*domain.Device, error)
	ListDevices(ctx context.Context, member domain.MemberID) ([]*domain.Device, error)
	ApproveDevice(ctx context.Context, id domain.DeviceID, approver domain.MemberID) error
	RevokeDevice(ctx context.Context, id domain.DeviceID) error
	GetMember(ctx context.Context, id domain.MemberID) (*domain.Member, error)
	ListMembers(ctx context.Context) ([]*domain.Member, error)
	ListIdentities(ctx context.Context) ([]*domain.Identity, error)
}

const deviceUsage = "usage: aether-server device <approve <code> | review> [--config <file>]"

func deviceCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(deviceUsage)
	}
	sub, args := args[0], args[1:]
	var code string
	switch sub {
	case "approve":
		if len(args) == 0 {
			return errors.New(deviceUsage)
		}
		code, args = args[0], args[1:]
	case "review":
	default:
		return fmt.Errorf("unknown device command %q (want approve or review)", sub)
	}
	o, _, rest, err := loadOptions("device "+sub, args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New(deviceUsage)
	}
	db, err := store.Open(server.StorePath(*o.dataDir))
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // every change is committed before Close
	if sub == "approve" {
		return deviceApprove(context.Background(), os.Stdout, db, code)
	}
	return deviceReview(context.Background(), os.Stdout, os.Stdin, db, edgeproto.AccessPolicy(*o.edgeAccess))
}

// deviceApprove approves a device awaiting approval as the machine's
// administrator, who is no member, so the approval names no approver.
func deviceApprove(ctx context.Context, w io.Writer, db deviceStore, code string) error {
	dev, err := db.GetDeviceByApprovalCode(ctx, code)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("no device is waiting for approval with code %q", code)
	case errors.Is(err, store.ErrConflict):
		return fmt.Errorf("%w; approve the right one with `sudo aether-server device review`", err)
	case err != nil:
		return err
	}
	member, err := db.GetMember(ctx, dev.Member)
	if err != nil {
		return err
	}
	if err := db.ApproveDevice(ctx, dev.ID, ""); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "approved device %q of %s (%s)\n", dev.Label, member.DisplayName, member.ID)
	return nil
}

// deviceReview walks the operator through every edge device awaiting
// approval, registered or pending, to approve, revoke or skip, then lists
// the SSH keys and tailnet identities that reach the server whatever
// edge-access says. A revocation here reaches a running server's live
// connections at its next revalidation, within seconds.
func deviceReview(ctx context.Context, w io.Writer, in io.Reader, db deviceStore, policy edgeproto.AccessPolicy) error {
	members, err := db.ListMembers(ctx)
	if err != nil {
		return err
	}
	byID := make(map[domain.MemberID]*domain.Member, len(members))
	for _, m := range members {
		byID[m.ID] = m
	}
	identities, err := db.ListIdentities(ctx)
	if err != nil {
		return err
	}
	logins := make(map[[2]string]string, len(identities))
	for _, id := range identities {
		logins[[2]string{id.Provider, id.Subject}] = cmp.Or(id.Login, id.Email)
	}
	devices, err := db.ListDevices(ctx, "")
	if err != nil {
		return err
	}
	var waiting []*domain.Device
	for _, dev := range devices {
		if dev.Status.AwaitsApproval() {
			waiting = append(waiting, dev)
		}
	}
	_, _ = fmt.Fprintf(w, "edge-access is %s. ", policy)
	if policy == edgeproto.PolicyAccount {
		_, _ = fmt.Fprintln(w, "Devices awaiting approval connect through the edge until you revoke them; approving them now keeps them working under approved-devices.")
	} else {
		_, _ = fmt.Fprintln(w, "Devices awaiting approval are refused until approved.")
	}
	if len(waiting) == 0 {
		_, _ = fmt.Fprintln(w, "\nNo device is awaiting approval.")
	}
	lines := bufio.NewReader(in)
	for i, dev := range waiting {
		name, member := string(dev.Member), byID[dev.Member]
		if member != nil {
			name = fmt.Sprintf("%s (%s)", member.DisplayName, member.ID)
		}
		account := dev.Provider + " " + cmp.Or(logins[[2]string{dev.Provider, dev.Subject}], dev.Subject)
		lastSeen := "never"
		if dev.LastSeenAt != nil {
			lastSeen = dev.LastSeenAt.Local().Format(time.DateTime)
		}
		_, _ = fmt.Fprintf(w, "\n%d. %q of %s, signed in as %s\n   %s, key %s\n   first seen %s, last seen %s\n",
			i+1, dev.Label, name, account, dev.Status, keyFingerprint(dev.Credential),
			dev.CreatedAt.Local().Format(time.DateTime), lastSeen)
		action, err := askDeviceAction(w, lines)
		if err != nil {
			return err
		}
		switch action {
		case "approve":
			err = db.ApproveDevice(ctx, dev.ID, "")
		case "revoke":
			err = db.RevokeDevice(ctx, dev.ID)
		}
		if err != nil {
			return fmt.Errorf("%s device %q: %w", action, dev.Label, err)
		}
		if action == "" {
			_, _ = fmt.Fprintln(w, "   skipped")
			continue
		}
		_, _ = fmt.Fprintf(w, "   %sd\n", action)
	}

	_, _ = fmt.Fprintln(w, "\nThese reach the server whatever edge-access says; an admin removes them with `aether member remove <id>`:")
	listed := false
	for _, m := range members {
		if m.PublicKey != "" {
			_, _ = fmt.Fprintf(w, "  SSH key %s of %s (%s), %s\n", keyFingerprint(m.PublicKey), m.DisplayName, m.ID, m.Role)
			listed = true
		}
		if m.TailnetLogin != "" {
			_, _ = fmt.Fprintf(w, "  tailnet login %s of %s (%s), %s\n", m.TailnetLogin, m.DisplayName, m.ID, m.Role)
			listed = true
		}
	}
	if !listed {
		_, _ = fmt.Fprintln(w, "  none")
	}
	return nil
}

// askDeviceAction returns "approve", "revoke", or "" to skip. Enter and
// the end of input skip, so nothing is approved without a typed answer.
func askDeviceAction(w io.Writer, lines *bufio.Reader) (string, error) {
	for {
		_, _ = fmt.Fprint(w, "   approve, revoke or skip? [a/r/S]: ")
		line, err := lines.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "a", "approve":
			return "approve", nil
		case "r", "revoke":
			return "revoke", nil
		case "", "s", "skip":
			if errors.Is(err, io.EOF) {
				_, _ = fmt.Fprintln(w)
			}
			return "", nil
		}
		if errors.Is(err, io.EOF) {
			return "", nil
		}
	}
}

// keyFingerprint is the SHA256 fingerprint of an authorized_keys line, or
// the line itself when it does not parse.
func keyFingerprint(line string) string {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return line
	}
	return ssh.FingerprintSHA256(key)
}
