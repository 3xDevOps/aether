package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/3xDevOps/Aether/internal/cli"
	edgeclient "github.com/3xDevOps/Aether/internal/edge/client"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

func init() {
	register(command{
		name:    "login",
		short:   "sign in to an edge to reach servers without SSH or network setup",
		usage:   "aether login [--edge <url>] [--label <name>] [--json]",
		example: "aether login --json",
		flags:   true,
		run:     runLogin,
	})
}

const edgeFlagUsage = "edge URL (default: the one edge you are signed in to, else " + edgeclient.DefaultURL + ")"

// edgeClient is the client for the --edge flag's value. Without one it is
// the only edge this machine is signed in to, or the project's edge.
func edgeClient(edgeURL string) (*edgeclient.Client, error) {
	dir, err := cli.Dir()
	if err != nil {
		return nil, err
	}
	return edgeclient.Choose(dir, edgeURL)
}

func runLogin(args []string) error {
	fs := newFlagSet("login")
	edgeURL := fs.String("edge", "", edgeFlagUsage)
	label := fs.String("label", "", "name of this device on the edge's Devices page (default: this machine's host name)")
	jsonOut := fs.Bool("json", false, "print the sign-in address and code as one JSON line, then the result as another; opens no browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: aether login [--edge <url>] [--label <name>] [--json]")
	}
	client, err := edgeClient(*edgeURL)
	if err != nil {
		return err
	}
	if *label == "" {
		if *label, err = os.Hostname(); err != nil {
			return fmt.Errorf("login: name this device with --label: %w", err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	login, err := client.StartLogin(ctx, *label)
	if err != nil {
		return err
	}
	if *jsonOut {
		return loginJSON(ctx, os.Stdout, client, login)
	}
	fmt.Printf("%s signs you in for the edge %s\n", edgeclient.HostOf(login.SigninOrigin), client.Host())
	fmt.Printf("open %s and enter the code %s\n", login.VerificationURI, login.UserCode)
	openBrowser(login.VerificationURI)
	fmt.Println("waiting for you to confirm the code...")
	session, err := client.Wait(ctx, login)
	if err != nil {
		return err
	}
	fmt.Printf("signed in to %s as %s; this device is %q\n", client.Host(), accountName(session.Account.Account), session.Device.Label)
	dir, err := cli.Dir()
	if err != nil {
		return err
	}
	fmt.Print(credentialLifetimes(filepath.Join(dir, edgeclient.TokensFile)))
	return nil
}

// loginLine's fields are those of the gateway's edge.login answer, plus
// ExpiresAt and Device.
type loginLine struct {
	State           string                 `json:"state"`
	Edge            string                 `json:"edge"`
	SigninOrigin    string                 `json:"signin_origin"`
	UserCode        string                 `json:"user_code"`
	VerificationURI string                 `json:"verification_uri"`
	ExpiresAt       string                 `json:"expires_at"`
	Account         *edgeproto.AccountInfo `json:"account,omitempty"`
	Device          *edgeproto.Device      `json:"device,omitempty"`
	Error           string                 `json:"error,omitempty"`
}

func loginJSON(ctx context.Context, w io.Writer, client *edgeclient.Client, login *edgeclient.Login) error {
	line := loginLine{
		State: "pending", Edge: client.URL(), SigninOrigin: login.SigninOrigin, UserCode: login.UserCode,
		VerificationURI: login.VerificationURI, ExpiresAt: login.Expires.UTC().Format(time.RFC3339),
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(line); err != nil {
		return err
	}
	session, waitErr := client.Wait(ctx, login)
	if waitErr != nil {
		line.State, line.Error = "failed", waitErr.Error()
	} else {
		line.State, line.Account, line.Device = "signed_in", &session.Account, &session.Device
	}
	if err := enc.Encode(line); err != nil {
		return err
	}
	return waitErr
}

// credentialLifetimes says how long what aether login leaves on this
// machine lasts, and what ends it. tokens is the file the token is in.
func credentialLifetimes(tokens string) string {
	return "the device token in " + tokens + " does not expire and is not refreshed. aether logout revokes it at\n" +
		"the edge and deletes it here; the edge's Devices page and deleting the account revoke it too. Your links stay:\n" +
		"the edge path refuses with the edge's reason, and a link's --addr still reaches the server with this device's\n" +
		"key until the account is deleted, which removes the account's devices on every server. SSH keys and tailnet\n" +
		"identities are not affected.\n"
}

// accountName is how a person recognizes their own account.
func accountName(a edgeproto.Account) string {
	switch {
	case a.Login != "":
		return a.Login + " (" + a.Provider + ")"
	case a.Email != "":
		return a.Email + " (" + a.Provider + ")"
	default:
		return a.Provider + " user " + a.Subject
	}
}
