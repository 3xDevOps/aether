package main

import (
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	edge "github.com/3xDevOps/Aether/internal/edge/service"
)

// reviewerCmd writes a reviewer account file with a new random
// password and prints the password, the only copy of it. Writing the file
// again replaces the password; a running edge reads it only at start.
func reviewerCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("aether-edge reviewer", flag.ContinueOnError)
	login := fs.String("login", "", "the reviewer's login: 1 to 39 lowercase letters, digits and dashes")
	serverID := fs.String("server-id", "", "the one server the reviewer is offered, as aether-edge servers list prints it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 || *login == "" || *serverID == "" {
		return fmt.Errorf("usage: aether-edge reviewer --login <login> --server-id <id> <file>")
	}
	password := rand.Text()
	hash, err := edge.HashReviewerPassword(password)
	if err != nil {
		return err
	}
	a := edge.ReviewerAccount{Login: *login, PasswordHash: hash, ServerID: *serverID}
	if err = a.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	path := fs.Arg(0)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".reviewer-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after the rename
	_, err = tmp.Write(append(raw, '\n'))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	_, err = fmt.Fprintf(out, "wrote %s for login %s on server %s\npassword: %s\n"+
		"restart the edge with --reviewer-account-file %s to use it; this is the only copy of the password\n",
		path, a.Login, a.ServerID, password, path)
	return err
}
