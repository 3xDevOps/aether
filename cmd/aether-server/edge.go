package main

import (
	"bufio"
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
	"github.com/3xDevOps/Aether/internal/edgeagent"
	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/3xDevOps/Aether/internal/server"
	"github.com/3xDevOps/Aether/internal/serversetup"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
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
		return edgeStatus(os.Stdout, *o.dataDir, edgeURL, time.Now())
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

func edgeStatus(w io.Writer, dataDir, edgeURL string, now time.Time) error {
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
	_, _ = fmt.Fprintf(tw, "dashboard\t%s\n", dashboardText(id, status, ran, owner != nil))
	return tw.Flush()
}

// dashboardText says where the dashboard through the edge is. Only the
// edge knows the domain it passes dashboards through under, and the
// server learns it when it connects.
func dashboardText(id string, status edgeagent.Status, ran, claimed bool) string {
	switch {
	case status.ServerDomain != "" && claimed:
		return "https://" + edgeproto.ServerHostname(id, status.ServerDomain) + "/"
	case status.ServerDomain != "":
		return "https://" + edgeproto.ServerHostname(id, status.ServerDomain) + "/ once the server is claimed"
	case ran && status.Connected:
		return "none; this edge passes no dashboard through"
	default:
		return "https://" + id + ".<the edge's server domain>/; aether-server edge status shows the address once the server connects"
	}
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
	printClaimCode(w, edgeURL, code, expires)
	return nil
}

func printClaimCode(w io.Writer, edgeURL, code string, expires time.Time) {
	_, _ = fmt.Fprintf(w, "claim code: %s (valid until %s, %d attempts)\n", code, expires.Local().Format(time.Kitchen), edgeproto.ClaimCodeAttempts)
	_, _ = fmt.Fprintf(w, "claim this server with:\n  aether link --claim %s\n", code)
	_, _ = fmt.Fprintf(w, "or enter it at %s%s\n", edgeURL, edgeproto.PathAddServer)
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
	key, err := edgeagent.FetchEdgeKey(context.Background(), edgeURL)
	if err != nil {
		return err
	}
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

// deviceApprover is the part of the store `device approve` uses.
type deviceApprover interface {
	GetDeviceByApprovalCode(ctx context.Context, code string) (*domain.Device, error)
	ApproveDevice(ctx context.Context, id domain.DeviceID, approver domain.MemberID) error
	GetMember(ctx context.Context, id domain.MemberID) (*domain.Member, error)
}

func deviceCmd(args []string) error {
	const usage = "usage: aether-server device approve <code> [--config <file>]"
	if len(args) < 2 || args[0] != "approve" {
		return errors.New(usage)
	}
	code := args[1]
	o, _, rest, err := loadOptions("device approve", args[2:])
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New(usage)
	}
	db, err := store.Open(server.StorePath(*o.dataDir))
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // the approval is committed before Close
	return deviceApprove(context.Background(), os.Stdout, db, code)
}

// deviceApprove approves a pending device as the machine's administrator,
// who is no member, so the approval names no approver.
func deviceApprove(ctx context.Context, w io.Writer, db deviceApprover, code string) error {
	dev, err := db.GetDeviceByApprovalCode(ctx, code)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no pending device has approval code %q", code)
	}
	if err != nil {
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
