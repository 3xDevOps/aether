package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	workspaceMirrorUsage          = "usage: aether workspace mirror <status|configure|refresh|adopt|disable>"
	workspaceMirrorStatusUsage    = "usage: aether workspace mirror status [--workspace <name-or-id>]"
	workspaceMirrorConfigureUsage = "usage: aether workspace mirror configure --source <url> [--workspace <name-or-id>] [--branch <branch>] [--auth public|deploy-key] [--known-hosts-file <path>]"
	workspaceMirrorRefreshUsage   = "usage: aether workspace mirror refresh --workspace <name-or-id>"
	workspaceMirrorAdoptUsage     = "usage: aether workspace mirror adopt --workspace <name-or-id> --generation <n> --yes"
	workspaceMirrorDisableUsage   = "usage: aether workspace mirror disable --workspace <name-or-id> --yes"
)

type workspaceMirrorConfigureOptions struct {
	workspace      string
	source         string
	branch         string
	auth           string
	knownHostsFile string
}

type workspaceMirrorAdoptOptions struct {
	workspace  string
	generation int64
	yes        bool
}

type workspaceMirrorWorkspaceOptions struct {
	workspace string
	yes       bool
}

func workspaceMirror(args []string) error {
	if len(args) < 1 {
		return errors.New(workspaceMirrorUsage)
	}
	switch args[0] {
	case "status":
		return workspaceMirrorStatus(args[1:])
	case "configure":
		return workspaceMirrorConfigure(args[1:])
	case "refresh":
		return workspaceMirrorRefresh(args[1:])
	case "adopt":
		return workspaceMirrorAdopt(args[1:])
	case "disable":
		return workspaceMirrorDisable(args[1:])
	default:
		return fmt.Errorf("unknown workspace mirror command %q", args[0])
	}
}

func parseWorkspaceMirrorStatusArgs(args []string) (string, error) {
	fs := flag.NewFlagSet("workspace mirror status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return "", errors.New(workspaceMirrorStatusUsage)
	}
	return *workspace, nil
}

func parseWorkspaceMirrorConfigureArgs(args []string) (workspaceMirrorConfigureOptions, error) {
	fs := flag.NewFlagSet("workspace mirror configure", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	source := fs.String("source", "", "upstream repository URL")
	branch := fs.String("branch", "", "upstream branch (default: the workspace base branch)")
	auth := fs.String("auth", "public", "upstream authentication: public or deploy-key")
	knownHostsFile := fs.String("known-hosts-file", "", "known_hosts text file for generic SSH deploy-key sources")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return workspaceMirrorConfigureOptions{}, errors.New(workspaceMirrorConfigureUsage)
	}
	if *source == "" {
		return workspaceMirrorConfigureOptions{}, fmt.Errorf("%s: --source is required", workspaceMirrorConfigureUsage)
	}
	if *auth != string(domain.MirrorAuthPublic) && *auth != string(domain.MirrorAuthDeployKey) {
		return workspaceMirrorConfigureOptions{}, fmt.Errorf("invalid --auth %q: want public or deploy-key", *auth)
	}
	return workspaceMirrorConfigureOptions{
		workspace:      *workspace,
		source:         *source,
		branch:         *branch,
		auth:           *auth,
		knownHostsFile: *knownHostsFile,
	}, nil
}

func parseWorkspaceMirrorRefreshArgs(args []string) (string, error) {
	fs := flag.NewFlagSet("workspace mirror refresh", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspace := fs.String("workspace", "", "workspace ID or name")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return "", errors.New(workspaceMirrorRefreshUsage)
	}
	if *workspace == "" {
		return "", fmt.Errorf("%s: --workspace is required", workspaceMirrorRefreshUsage)
	}
	return *workspace, nil
}

func parseWorkspaceMirrorAdoptArgs(args []string) (workspaceMirrorAdoptOptions, error) {
	fs := flag.NewFlagSet("workspace mirror adopt", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspace := fs.String("workspace", "", "workspace ID or name")
	generation := fs.Int64("generation", 0, "candidate mirror generation to adopt")
	yes := fs.Bool("yes", false, "confirm replacing the accepted base with the candidate")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return workspaceMirrorAdoptOptions{}, errors.New(workspaceMirrorAdoptUsage)
	}
	if *workspace == "" {
		return workspaceMirrorAdoptOptions{}, fmt.Errorf("%s: --workspace is required", workspaceMirrorAdoptUsage)
	}
	if *generation <= 0 {
		return workspaceMirrorAdoptOptions{}, fmt.Errorf("%s: --generation must be greater than zero", workspaceMirrorAdoptUsage)
	}
	if !*yes {
		return workspaceMirrorAdoptOptions{}, fmt.Errorf("%s: --yes is required because adopt replaces the accepted base", workspaceMirrorAdoptUsage)
	}
	return workspaceMirrorAdoptOptions{workspace: *workspace, generation: *generation, yes: *yes}, nil
}

func parseWorkspaceMirrorDisableArgs(args []string) (workspaceMirrorWorkspaceOptions, error) {
	return parseWorkspaceMirrorWorkspaceArgs(args, "workspace mirror disable", workspaceMirrorDisableUsage)
}

func parseWorkspaceMirrorWorkspaceArgs(args []string, name, usage string) (workspaceMirrorWorkspaceOptions, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspace := fs.String("workspace", "", "workspace ID or name")
	yes := fs.Bool("yes", false, "confirm disabling workspace mirroring")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return workspaceMirrorWorkspaceOptions{}, errors.New(usage)
	}
	if *workspace == "" {
		return workspaceMirrorWorkspaceOptions{}, fmt.Errorf("%s: --workspace is required", usage)
	}
	if !*yes {
		return workspaceMirrorWorkspaceOptions{}, fmt.Errorf("%s: --yes is required because disabling removes the server mirror", usage)
	}
	return workspaceMirrorWorkspaceOptions{workspace: *workspace, yes: *yes}, nil
}

func workspaceMirrorStatus(args []string) error {
	workspace, err := parseWorkspaceMirrorStatusArgs(args)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		ws, err := resolveMirrorWorkspace(c, workspace, false)
		if err != nil {
			return err
		}
		var result protocol.WorkspaceMirrorResult
		if err := c.Call(protocol.MethodWorkspaceMirrorStatus, protocol.WorkspaceMirrorParams{WorkspaceID: ws.ID}, &result); err != nil {
			return err
		}
		return printWorkspaceMirror(os.Stdout, ws, result)
	})
}

func workspaceMirrorConfigure(args []string) error {
	opts, err := parseWorkspaceMirrorConfigureArgs(args)
	if err != nil {
		return err
	}
	knownHosts, err := readMirrorKnownHosts(opts.knownHostsFile)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		ws, err := resolveMirrorWorkspace(c, opts.workspace, false)
		if err != nil {
			return err
		}
		branch := opts.branch
		if branch == "" {
			branch = ws.BaseBranch
			if branch == "" {
				branch = domain.DefaultBaseBranch
			}
		}
		var result protocol.WorkspaceMirrorResult
		if err := c.Call(protocol.MethodWorkspaceMirrorConfigure, protocol.WorkspaceMirrorConfigureParams{
			WorkspaceID: ws.ID,
			SourceURL:   opts.source,
			Branch:      branch,
			Auth:        opts.auth,
			KnownHosts:  knownHosts,
		}, &result); err != nil {
			return err
		}
		if err := printWorkspaceMirror(os.Stdout, ws, result); err != nil {
			return err
		}
		return printMirrorDeployKeyInstructions(os.Stdout, result)
	})
}

func workspaceMirrorRefresh(args []string) error {
	workspace, err := parseWorkspaceMirrorRefreshArgs(args)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		ws, err := resolveMirrorWorkspace(c, workspace, true)
		if err != nil {
			return err
		}
		var result protocol.WorkspaceMirrorResult
		if err := c.Call(protocol.MethodWorkspaceMirrorRefresh, protocol.WorkspaceMirrorParams{WorkspaceID: ws.ID}, &result); err != nil {
			return err
		}
		return printWorkspaceMirror(os.Stdout, ws, result)
	})
}

func workspaceMirrorAdopt(args []string) error {
	opts, err := parseWorkspaceMirrorAdoptArgs(args)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		ws, err := resolveMirrorWorkspace(c, opts.workspace, true)
		if err != nil {
			return err
		}
		var result protocol.WorkspaceMirrorResult
		if err := c.Call(protocol.MethodWorkspaceMirrorAdopt, protocol.WorkspaceMirrorAdoptParams{
			WorkspaceID: ws.ID,
			Generation:  opts.generation,
		}, &result); err != nil {
			return err
		}
		return printWorkspaceMirror(os.Stdout, ws, result)
	})
}

func workspaceMirrorDisable(args []string) error {
	opts, err := parseWorkspaceMirrorDisableArgs(args)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		ws, err := resolveMirrorWorkspace(c, opts.workspace, true)
		if err != nil {
			return err
		}
		var result protocol.WorkspaceMirrorResult
		if callErr := c.Call(protocol.MethodWorkspaceMirrorDisable, protocol.WorkspaceMirrorParams{WorkspaceID: ws.ID}, &result); callErr != nil {
			return callErr
		}
		if printErr := printWorkspaceMirror(os.Stdout, ws, result); printErr != nil {
			return printErr
		}
		warning := result.Warning
		if warning == "" {
			warning = "if this mirror used a deploy key, revoke that key from the external source repository"
		}
		if result.Warning != "" {
			// printWorkspaceMirror already rendered the service's precise warning.
			return nil
		}
		_, err = fmt.Fprintf(os.Stdout, "warning: %s\n", warning)
		return err
	})
}

func resolveMirrorWorkspace(c *protocol.Client, idOrName string, explicit bool) (protocol.Workspace, error) {
	if explicit && idOrName == "" {
		return protocol.Workspace{}, errors.New("--workspace is required")
	}
	var list protocol.WorkspaceListResult
	if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &list); err != nil {
		return protocol.Workspace{}, err
	}
	id, err := pickWorkspace(list.Workspaces, idOrName)
	if err != nil {
		return protocol.Workspace{}, err
	}
	ws, ok := workspaceByID(list.Workspaces, id)
	if !ok {
		return protocol.Workspace{}, fmt.Errorf("workspace %q not found", id)
	}
	return ws, nil
}

func readMirrorKnownHosts(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read known-hosts file %q: %w", path, err)
	}
	// A known_hosts file is public host-key data. Refuse obvious private-key
	// material before it can cross the control channel, even if the user
	// accidentally points this flag at an SSH private-key file.
	lower := strings.ToLower(string(data))
	if strings.Contains(lower, "private key") ||
		strings.Contains(lower, "private-key") ||
		strings.Contains(lower, "begin openssh") ||
		strings.Contains(lower, "begin rsa") ||
		strings.Contains(lower, "begin ecdsa") ||
		strings.Contains(lower, "begin ed25519") {
		return "", errors.New("known-hosts file appears to contain private key material")
	}
	return string(data), nil
}

func printWorkspaceMirror(w io.Writer, ws protocol.Workspace, result protocol.WorkspaceMirrorResult) error {
	if _, err := fmt.Fprintf(w, "workspace %s %s\n", ws.ID, ws.Name); err != nil {
		return err
	}
	if !result.Enabled {
		if _, err := fmt.Fprintln(w, "mirror local-only"); err != nil {
			return err
		}
		return printMirrorWarning(w, result.Warning)
	}
	for _, field := range []struct{ label, value string }{
		{"mirror", "enabled"},
		{"source", result.SourceURL},
		{"source identity", result.SourceIdentity},
		{"branch", result.Branch},
		{"auth", result.Auth},
		{"status", result.Status},
		{"generation", strconv.FormatInt(result.Generation, 10)},
		{"observed SHA", result.ObservedCommit},
		{"accepted SHA", result.AcceptedCommit},
		{"key fingerprint", result.KeyFingerprint},
		{"created", result.CreatedAt},
		{"updated", result.UpdatedAt},
	} {
		if field.value == "" {
			continue
		}
		if _, err := fmt.Fprintf(w, "%s %s\n", field.label, field.value); err != nil {
			return err
		}
	}
	if result.LastAttemptAt != nil {
		if _, err := fmt.Fprintf(w, "last check %s\n", *result.LastAttemptAt); err != nil {
			return err
		}
	}
	if result.LastSuccessAt != nil {
		if _, err := fmt.Fprintf(w, "last success %s\n", *result.LastSuccessAt); err != nil {
			return err
		}
	}
	if result.LastError != "" {
		if _, err := fmt.Fprintf(w, "last error %s\n", result.LastError); err != nil {
			return err
		}
	}
	if result.PublicKey != "" {
		if _, err := fmt.Fprintf(w, "public deploy key %s\n", result.PublicKey); err != nil {
			return err
		}
	}
	return printMirrorWarning(w, result.Warning)
}

func printMirrorWarning(w io.Writer, warning string) error {
	if warning == "" {
		return nil
	}
	_, err := fmt.Fprintf(w, "warning: %s\n", warning)
	return err
}

func printMirrorDeployKeyInstructions(w io.Writer, result protocol.WorkspaceMirrorResult) error {
	if result.PublicKey == "" {
		return nil
	}
	if _, err := fmt.Fprintln(w, "install this public deploy key as read-only at the source repository"); err != nil {
		return err
	}
	if githubURL := githubDeployKeyURL(result.SourceURL); githubURL != "" {
		if _, err := fmt.Fprintf(w, "GitHub deploy-key settings: %s\n", githubURL); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, "Open Settings > Deploy keys > Add deploy key, paste the public key above, and leave Allow write access disabled."); err != nil {
			return err
		}
	}
	return nil
}

func githubDeployKeyURL(source string) string {
	owner, repo, ok := githubRepository(source)
	if !ok {
		return ""
	}
	return "https://github.com/" + owner + "/" + repo + "/settings/keys/new"
}

func githubRepository(source string) (string, string, bool) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", "", false
	}
	if strings.HasPrefix(source, "git@github.com:") {
		source = "ssh://github.com/" + strings.TrimPrefix(source, "git@github.com:")
	}
	u, err := url.Parse(source)
	if err != nil {
		return "", "", false
	}
	if !strings.EqualFold(u.Hostname(), "github.com") {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 {
		return "", "", false
	}
	owner, err1 := url.PathUnescape(parts[0])
	repo, err2 := url.PathUnescape(parts[1])
	if err1 != nil || err2 != nil {
		return "", "", false
	}
	repo = strings.TrimSuffix(repo, ".git")
	for _, part := range []string{owner, repo} {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00\r\n\t /") {
			return "", "", false
		}
	}
	return owner, repo, true
}
