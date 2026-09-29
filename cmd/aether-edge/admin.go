package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	edgestore "github.com/3xDevOps/Aether/internal/edge/store"
)

// The operator commands work on the data directory's database and are
// safe while the edge runs: each change is one SQLite transaction, and
// the edge reads the rows they change on every enrollment, sign-in and
// connection. docs/edge.md#operator-commands says what a change does to
// connections already open.
//
// They list, remove and block. None grants: no command approves a
// device, records an owner or admits an account to a server, so the
// edge's operator has no way to let anyone in that the servers did not.
// TestOperatorCommandsOnlyRemoveAndBlock holds the list.

const serversUsage = `usage: aether-edge servers <command> [--data <dir>] [<server id>]

commands:
  list               claimed and blocked servers
  remove <id>        forget a claimed server; its administrator can claim it again
  block <id>         forget the server and refuse its id from now on
  unblock <id>       accept the server id again
`

const accountsUsage = `usage: aether-edge accounts <command> [--data <dir>] [<provider>:<subject>]

commands:
  list               accounts and blocked accounts
  block <account>    refuse the account's sign-ins and delete its devices and sessions
  unblock <account>  accept the account's sign-ins again
  delete <account>   delete the account as its account page does; a block stays

<account> is github:<user id> or google:<subject>, as list prints it.
`

// errUsage reports a command line that does not name a command; the usage
// has been printed.
var errUsage = errors.New("usage")

var serverCommands = map[string]adminCommand{
	"list":    {run: listServers},
	"remove":  {arg: checkServerID, run: removeServer},
	"block":   {arg: checkServerID, run: blockServer},
	"unblock": {arg: checkServerID, run: unblockServer},
}

var accountCommands = map[string]adminCommand{
	"list":    {run: listAccounts},
	"block":   {arg: checkAccount, run: blockAccount},
	"unblock": {arg: checkAccount, run: unblockAccount},
	"delete":  {arg: checkAccount, run: deleteAccount},
}

func servers(args []string, getenv func(string) string, out io.Writer) error {
	return admin("servers", serversUsage, args, getenv, out, serverCommands)
}

func accounts(args []string, getenv func(string) string, out io.Writer) error {
	return admin("accounts", accountsUsage, args, getenv, out, accountCommands)
}

// adminCommand is one operator command. arg, when set, checks the single
// argument the command takes.
type adminCommand struct {
	arg func(string) error
	run func(ctx context.Context, s *edgestore.Store, arg string, out io.Writer) error
}

func admin(group, usage string, args []string, getenv func(string) string, out io.Writer, cmds map[string]adminCommand) error {
	if len(args) == 0 {
		_, _ = fmt.Fprint(os.Stderr, usage)
		return errUsage
	}
	cmd, ok := cmds[args[0]]
	if !ok {
		_, _ = fmt.Fprintf(os.Stderr, "aether-edge: unknown command %q\n%s", group+" "+args[0], usage)
		return errUsage
	}
	fs := flag.NewFlagSet("aether-edge "+group+" "+args[0], flag.ContinueOnError)
	dataDir := fs.String("data", dataDirDefault(getenv), "data directory of the edge")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	want := 0
	if cmd.arg != nil {
		want = 1
	}
	if fs.NArg() != want {
		return fmt.Errorf("aether-edge %s %s takes %d argument(s) after its flags, got %d", group, args[0], want, fs.NArg())
	}
	arg := fs.Arg(0)
	if cmd.arg != nil {
		if err := cmd.arg(arg); err != nil {
			return err
		}
	}
	s, err := openStore(*dataDir)
	if err != nil {
		return err
	}
	return errors.Join(cmd.run(context.Background(), s, arg, out), s.Close())
}

// openStore opens the database of an existing edge. It never creates
// one, and runs only as the database's owner: SQLite creates its -wal and
// -shm files as whoever opens the database first, and a root-owned one
// would lock the edge out of its own database.
func openStore(dataDir string) (*edgestore.Store, error) {
	path := filepath.Join(dataDir, "edge.db")
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("no edge database: %w; name the data directory with --data or AETHER_EDGE_DATA", err)
	}
	if err := checkOwner(path, info); err != nil {
		return nil, err
	}
	return edgestore.Open(path)
}

func checkServerID(id string) error {
	if !edgeproto.ValidServerID(id) {
		return fmt.Errorf("%q is not a server id: 26 characters of a-z and 2-7", id)
	}
	return nil
}

func checkAccount(key string) error {
	_, _, err := splitAccount(key)
	return err
}

func splitAccount(key string) (provider, subject string, err error) {
	provider, subject, ok := strings.Cut(key, ":")
	if !ok || !edgeproto.ValidProvider(provider) || subject == "" {
		return "", "", fmt.Errorf("%q is not an account: write github:<user id> or google:<subject>, as aether-edge accounts list prints it", key)
	}
	return provider, subject, nil
}

func listServers(ctx context.Context, s *edgestore.Store, _ string, out io.Writer) error {
	rows, err := s.ListServers(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SERVER ID\tSTATE\tNAME\tOWNER\tOWNER LOGIN OR EMAIL\tSINCE") //nolint:errcheck // Flush reports it
	for _, r := range rows {
		state, since, owner := "claimed", r.ClaimedAt, accountKey(r.Owner)
		switch {
		case !r.BlockedAt.IsZero():
			state, since, owner = "blocked", r.BlockedAt, ""
		case owner == "":
			state = "ownerless"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, state, dash(r.Name), dash(owner), //nolint:errcheck // Flush reports it
			dash(contact(r.Owner)), since.Format(time.RFC3339))
	}
	return w.Flush()
}

func removeServer(ctx context.Context, s *edgestore.Store, id string, out io.Writer) error {
	err := s.DeleteServer(ctx, id)
	if errors.Is(err, edgestore.ErrNotFound) {
		return fmt.Errorf("server %s is not claimed at this edge", id)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "removed server %s\n", id)
	return err
}

func blockServer(ctx context.Context, s *edgestore.Store, id string, out io.Writer) error {
	if err := s.BlockServer(ctx, id, time.Now()); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "blocked server %s\n", id)
	return err
}

func unblockServer(ctx context.Context, s *edgestore.Store, id string, out io.Writer) error {
	err := s.UnblockServer(ctx, id)
	if errors.Is(err, edgestore.ErrNotFound) {
		return fmt.Errorf("server %s is not blocked", id)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "unblocked server %s\n", id)
	return err
}

func listAccounts(ctx context.Context, s *edgestore.Store, _ string, out io.Writer) error {
	rows, err := s.ListAccounts(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ACCOUNT\tSTATE\tLOGIN\tEMAIL\tDEVICES\tSERVERS OWNED\tCREATED") //nolint:errcheck // Flush reports it
	for _, r := range rows {
		state, created := "active", "-"
		if r.Blocked {
			state = "blocked"
		}
		if !r.CreatedAt.IsZero() {
			created = r.CreatedAt.Format(time.RFC3339)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\t%s\n", accountKey(r.Account), state, //nolint:errcheck // Flush reports it
			dash(r.Account.Login), dash(r.Account.Email), r.Devices, r.Servers, created)
	}
	return w.Flush()
}

func blockAccount(ctx context.Context, s *edgestore.Store, key string, out io.Writer) error {
	provider, subject, _ := splitAccount(key) // checkAccount checked it
	if err := s.BlockAccount(ctx, provider, subject, time.Now()); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "blocked account %s\n", key)
	return err
}

func unblockAccount(ctx context.Context, s *edgestore.Store, key string, out io.Writer) error {
	provider, subject, _ := splitAccount(key)
	err := s.UnblockAccount(ctx, provider, subject)
	if errors.Is(err, edgestore.ErrNotFound) {
		return fmt.Errorf("account %s is not blocked", key)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "unblocked account %s\n", key)
	return err
}

func deleteAccount(ctx context.Context, s *edgestore.Store, key string, out io.Writer) error {
	provider, subject, _ := splitAccount(key)
	d, err := s.DeleteAccount(ctx, provider, subject, time.Now())
	if errors.Is(err, edgestore.ErrNotFound) {
		return fmt.Errorf("account %s does not exist at this edge", key)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "deleted account %s (%s): %d devices; ownerless now: %v; membership entries removed on: %v; "+
		"sent the deletion when each next enrolls: %v\n",
		key, dash(contact(d.Account.Account)), d.Devices, d.Owned, d.Member, d.Notify)
	return err
}

func accountKey(a edgeproto.Account) string {
	if a.Provider == "" {
		return ""
	}
	return a.Provider + ":" + a.Subject
}

// contact is what identifies an account to a person: its login, or its
// email for an account without one.
func contact(a edgeproto.Account) string {
	if a.Login != "" {
		return a.Login
	}
	return a.Email
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
