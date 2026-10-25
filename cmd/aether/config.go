package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	profilesvc "github.com/3xDevOps/Aether/internal/profile"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/secretscan"
)

func init() {
	register(command{
		name:  "config",
		short: "list and upload agent configuration",
		usage: "aether config ls\n" +
			"aether config ls [-r] <agent> [path]\n" +
			"aether config cat <agent> <path>\n" +
			"aether config push [--dry-run] [--overwrite] <agent> <path>...\n" +
			"aether config push [--dry-run] [--overwrite] <agent> <path> --from <file>",
		example: "aether config ls\n" +
			"aether config ls -r claude\n" +
			"aether config cat claude settings.json\n" +
			"aether config push --dry-run claude settings.json",
		run: runConfig,
	})
}

const configUsage = "usage: aether config ls\n" +
	"   or: aether config ls [-r] <agent> [path]\n" +
	"   or: aether config cat <agent> <path>\n" +
	"   or: aether config push [--dry-run] [--overwrite] <agent> <path>...\n" +
	"   or: aether config push [--dry-run] [--overwrite] <agent> <path> --from <file>\n" +
	"each path is a file or directory inside the agent's directory, relative to it; . is the whole directory\n" +
	"ls and cat read the server's copy; push uploads this computer's, or with --from uploads <file> as <path>\n" +
	"push never replaces a server file that differs unless --overwrite is given"

const (
	// configMaxFileBytes mirrors memberhome.ConfigMaxFileBytes, which the
	// server enforces per file and per config.import request.
	configMaxFileBytes = 64 << 20
	configBatchFiles   = 2000
	configBatchBytes   = 20 << 20
)

type configPushOptions struct {
	dryRun, overwrite bool
	// from is the file uploaded as the one path argument, when set.
	from string
}

func runConfig(args []string) error {
	if len(args) == 0 {
		return errors.New(configUsage)
	}
	switch args[0] {
	case "help":
		fmt.Println(configUsage)
		return nil
	case "ls":
		var recursive bool
		var positional []string
		for _, arg := range args[1:] {
			switch {
			case arg == "-r":
				recursive = true
			case strings.HasPrefix(arg, "-"):
				return fmt.Errorf("unknown flag %s\n%s", arg, configUsage)
			default:
				positional = append(positional, arg)
			}
		}
		switch {
		case len(positional) == 0 && !recursive:
			return withControl(func(c *protocol.Client) error { return configList(c, os.Stdout) })
		case len(positional) == 1 || len(positional) == 2:
			dir := ""
			if len(positional) == 2 {
				dir = positional[1]
			}
			return withControl(func(c *protocol.Client) error {
				return configTree(c, os.Stdout, positional[0], dir, recursive)
			})
		}
		return errors.New(configUsage)
	case "cat":
		if len(args) != 3 {
			return errors.New(configUsage)
		}
		return withControl(func(c *protocol.Client) error { return configCat(c, os.Stdout, args[1], args[2]) })
	case "push":
		var opts configPushOptions
		var positional []string
		for rest := args[1:]; len(rest) > 0; rest = rest[1:] {
			switch arg := rest[0]; {
			case arg == "--dry-run" || arg == "-dry-run":
				opts.dryRun = true
			case arg == "--overwrite":
				opts.overwrite = true
			case arg == "--from" && len(rest) > 1:
				opts.from = rest[1]
				rest = rest[1:]
			case strings.HasPrefix(arg, "-"):
				return fmt.Errorf("unknown flag %s\n%s", arg, configUsage)
			default:
				positional = append(positional, arg)
			}
		}
		if len(positional) < 2 || (opts.from != "" && len(positional) != 2) {
			return errors.New(configUsage)
		}
		return withControl(func(c *protocol.Client) error {
			return configPush(c, os.Stdout, positional[0], positional[1:], opts)
		})
	}
	return fmt.Errorf("unknown config command %q\n%s", args[0], configUsage)
}

func configRoots(c *protocol.Client) ([]protocol.ConfigRoot, error) {
	var result protocol.ConfigRootsResult
	if err := c.Call(protocol.MethodConfigRoots, struct{}{}, &result); err != nil {
		return nil, err
	}
	return result.Roots, nil
}

func configRoot(c *protocol.Client, agent string) (protocol.ConfigRoot, error) {
	roots, err := configRoots(c)
	if err != nil {
		return protocol.ConfigRoot{}, err
	}
	for _, root := range roots {
		if root.Harness == agent {
			return root, nil
		}
	}
	return protocol.ConfigRoot{}, fmt.Errorf("aether holds no configuration for agent %q; aether config ls lists the agents it does", agent)
}

func configLocalDir(root protocol.ConfigRoot) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(root.Path, "~/"))), nil
}

func configList(c *protocol.Client, out io.Writer) error {
	roots, err := configRoots(c)
	if err != nil {
		return err
	}
	var agents protocol.AgentListResult
	if err := c.Call(protocol.MethodAgentList, struct{}{}, &agents); err != nil {
		return err
	}
	installed := map[string]string{}
	for _, agent := range agents.Agents {
		installed[agent.Name] = ", not installed on the server"
		if agent.Installed {
			installed[agent.Name] = ", installed on the server"
		}
	}
	for _, root := range roots {
		dir, err := configLocalDir(root)
		if err != nil {
			return err
		}
		here := "exists on this computer"
		if _, statErr := os.Stat(dir); errors.Is(statErr, fs.ErrNotExist) {
			here = "does not exist on this computer"
		} else if statErr != nil {
			return statErr
		}
		credentials := []string{}
		seen := map[string]bool{}
		for _, name := range append(root.CredentialNames, "*.pem", ".env", ".env.*") {
			if !seen[name] {
				seen[name] = true
				credentials = append(credentials, name)
			}
		}
		runtimePaths := "none"
		if len(root.RuntimeIgnores) > 0 {
			runtimePaths = strings.Join(root.RuntimeIgnores, ", ")
		}
		_, _ = fmt.Fprintf(out, "%s (%s): %s, %s%s\n", root.Harness, root.DisplayName, root.Path, here, installed[root.Harness])
		_, _ = fmt.Fprintf(out, "  always left out, credentials: %s\n", strings.Join(credentials, ", "))
		_, _ = fmt.Fprintf(out, "  always left out, runtime and history: %s\n", runtimePaths)
	}
	_, _ = fmt.Fprintln(out, "always left out, every agent: symlinks, special files, files over 64 MiB, .aether-profile-ignore, and files the secret scanner flags")
	return nil
}

func configTree(c *protocol.Client, out io.Writer, agent, dir string, recursive bool) error {
	root, err := configRoot(c, agent)
	if err != nil {
		return err
	}
	rel := "."
	if dir != "" {
		if rel, err = configRel(root, dir); err != nil {
			return err
		}
	}
	return configTreeAt(c, out, root, rel, "", recursive)
}

// configTreeAt prints what the server has in rel, each name after prefix.
func configTreeAt(c *protocol.Client, out io.Writer, root protocol.ConfigRoot, rel, prefix string, recursive bool) error {
	params := protocol.ConfigTreeParams{Harness: root.Harness}
	if rel != "." {
		params.Path = configServerPath(root, rel)
	}
	var result protocol.ConfigTreeResult
	if err := c.Call(protocol.MethodConfigTree, params, &result); err != nil {
		return err
	}
	for _, entry := range result.Entries {
		if entry.Kind != "dir" {
			_, _ = fmt.Fprintf(out, "file\t%d\t%s%s\n", entry.Size, prefix, entry.Name)
			continue
		}
		_, _ = fmt.Fprintf(out, "dir\t%s%s\n", prefix, entry.Name)
		if recursive {
			if err := configTreeAt(c, out, root, path.Join(rel, entry.Name), prefix+entry.Name+"/", true); err != nil {
				return err
			}
		}
	}
	return nil
}

func configCat(c *protocol.Client, out io.Writer, agent, file string) error {
	root, err := configRoot(c, agent)
	if err != nil {
		return err
	}
	rel, err := configRel(root, file)
	if err != nil {
		return err
	}
	var result protocol.ConfigFileReadResult
	if err = c.Call(protocol.MethodConfigRead, protocol.ConfigReadParams{Harness: root.Harness, Path: configServerPath(root, rel)}, &result); err != nil {
		return err
	}
	if result.Binary || result.Truncated {
		return fmt.Errorf("%s is %d bytes that are not text; aether config cat prints text files of up to 64 MiB", rel, result.Size)
	}
	_, err = io.WriteString(out, result.Content)
	return err
}

// configRel resolves a path argument against the agent's directory on this
// computer and returns it slash-separated and relative to that directory.
func configRel(root protocol.ConfigRoot, arg string) (string, error) {
	dir, err := configLocalDir(root)
	if err != nil {
		return "", err
	}
	abs := arg
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(dir, abs)
	}
	rel, err := filepath.Rel(dir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside %s", arg, root.Path)
	}
	return filepath.ToSlash(rel), nil
}

// configServerPath prefixes rel with the agent's directory relative to home.
// The server strips one such prefix, so a rel that starts with the
// directory's own name, such as .claude/settings.json, would otherwise land
// one level up.
func configServerPath(root protocol.ConfigRoot, rel string) string {
	return strings.TrimPrefix(root.Path, "~/") + "/" + rel
}

// configFile is a file push will read; rel is slash-separated and relative
// to the agent's directory. source, when set, is the file read in its place.
type configFile struct {
	rel    string
	mode   uint32
	source string
}

type configLeftOut struct {
	rel, reason string
}

// configPolicyReason says why rel is left out whatever it holds, or "".
func configPolicyReason(root protocol.ConfigRoot, rel string) string {
	switch {
	case profilesvc.DeniedBasename(rel, root.CredentialNames):
		return "credential"
	case rel == ".aether-profile-ignore":
		return "Aether profile ignore file"
	case profilesvc.Ignored(root.RuntimeIgnores, rel):
		return "runtime or history path"
	}
	return ""
}

func configMode(info fs.FileInfo) uint32 {
	if runtime.GOOS == "windows" {
		return 0
	}
	return uint32(info.Mode().Perm())
}

// walkConfig resolves each path argument against the agent's directory,
// whose symlinks resolve to resolved, and classifies everything under it
// without reading file contents.
func walkConfig(root protocol.ConfigRoot, resolved string, paths []string) ([]configFile, []configLeftOut, error) {
	var files []configFile
	var leftOut []configLeftOut
	seen := map[string]bool{}
	starts := make([]string, 0, len(paths))
args:
	for _, arg := range paths {
		rel, err := configRel(root, arg)
		if err != nil {
			return nil, nil, err
		}
		var component string
		for _, name := range strings.Split(rel, "/") {
			component = path.Join(component, name)
			info, err := os.Lstat(filepath.Join(resolved, filepath.FromSlash(component)))
			if err != nil {
				return nil, nil, err
			}
			if info.Mode()&fs.ModeSymlink != 0 {
				if !seen[rel] {
					seen[rel] = true
					leftOut = append(leftOut, configLeftOut{rel: rel, reason: fmt.Sprintf("symlink %s, not followed", component)})
				}
				continue args
			}
		}
		starts = append(starts, rel)
	}
	for _, start := range starts {
		err := filepath.WalkDir(filepath.Join(resolved, filepath.FromSlash(start)), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(resolved, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if rel == "." {
				return nil
			}
			if seen[rel] {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			seen[rel] = true
			reason := configPolicyReason(root, rel)
			switch {
			case reason != "":
			case d.Type()&fs.ModeSymlink != 0:
				reason = "symlink, not followed"
			case d.IsDir():
				return nil
			case !d.Type().IsRegular():
				reason = "not a regular file"
			}
			if reason != "" {
				if d.IsDir() {
					leftOut = append(leftOut, configLeftOut{rel: rel + "/", reason: reason})
					return filepath.SkipDir
				}
				leftOut = append(leftOut, configLeftOut{rel: rel, reason: reason})
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Size() > configMaxFileBytes {
				leftOut = append(leftOut, configLeftOut{rel: rel, reason: configTooLarge(info.Size())})
				return nil
			}
			files = append(files, configFile{rel: rel, mode: configMode(info)})
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return files, leftOut, nil
}

func configTooLarge(size int64) string {
	return fmt.Sprintf("%d bytes is over the 64 MiB upload limit; copy it onto the server from aether terminal", size)
}

// configRead reads f through local, which refuses a file swapped for a
// symlink out of the agent's directory after the walk, and stops one byte
// past the upload limit in case the file grew after the walk. size is the
// file's size when it was read.
func configRead(local *os.Root, f configFile) (content []byte, size int64, err error) {
	var file *os.File
	if f.source != "" {
		file, err = os.Open(f.source)
	} else {
		file, err = local.Open(filepath.FromSlash(f.rel))
	}
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = file.Close() }()
	content, err = io.ReadAll(io.LimitReader(file, configMaxFileBytes+1))
	if err != nil {
		return nil, 0, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	return content, info.Size(), nil
}

// configPushItem is one file of a batch. state is what happened to it, in
// the words push prints; content is kept only while the file may be sent.
type configPushItem struct {
	rel, state, detail string
	mode               uint32
	content            []byte
	sent               bool
}

const (
	pushCreated  = "created"
	pushReplaced = "replaced"
	pushSkipped  = "skipped"
	pushRefused  = "refused"
	pushLeftOut  = "left out"
)

var pushWould = map[string]string{
	pushCreated:  "would create",
	pushReplaced: "would replace",
	pushSkipped:  "would skip",
	pushRefused:  "would refuse",
	pushLeftOut:  "would leave out",
}

func configPush(c *protocol.Client, out io.Writer, agent string, paths []string, opts configPushOptions) error {
	root, err := configRoot(c, agent)
	if err != nil {
		return err
	}
	var files []configFile
	var leftOut []configLeftOut
	var local *os.Root
	if opts.from != "" {
		rel, err := configRel(root, paths[0])
		if err != nil {
			return err
		}
		if rel == "." {
			return fmt.Errorf("--from uploads one file, and %s is the agent's directory", paths[0])
		}
		info, err := os.Stat(opts.from)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", opts.from)
		}
		if reason := configPolicyReason(root, rel); reason != "" {
			leftOut = append(leftOut, configLeftOut{rel: rel, reason: reason})
		} else {
			files = append(files, configFile{rel: rel, mode: configMode(info), source: opts.from})
		}
	} else {
		dir, err := configLocalDir(root)
		if err != nil {
			return err
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return err
		}
		if files, leftOut, err = walkConfig(root, resolved, paths); err != nil {
			return err
		}
		if local, err = os.OpenRoot(resolved); err != nil {
			return err
		}
		defer func() { _ = local.Close() }()
	}

	counts := map[string]int{}
	var uploadBytes int64
	report := func(state, rel, detail string) {
		counts[state]++
		if opts.dryRun {
			state = pushWould[state]
		}
		if detail != "" {
			detail = ": " + detail
		}
		_, _ = fmt.Fprintf(out, "%s %s%s\n", state, rel, detail)
	}
	for _, l := range leftOut {
		report(pushLeftOut, l.rel, l.reason)
	}

	var batch []configPushItem
	var batchFiles, batchBytes int
	flush := func() error {
		var statPaths []string
		for _, item := range batch {
			if item.state == "" {
				statPaths = append(statPaths, configServerPath(root, item.rel))
			}
		}
		onServer := map[string]protocol.ConfigStatEntry{}
		if len(statPaths) > 0 {
			var stat protocol.ConfigStatResult
			if err := c.Call(protocol.MethodConfigStat, protocol.ConfigStatParams{Harness: root.Harness, Paths: statPaths}, &stat); err != nil {
				var rpc *protocol.Error
				if errors.As(err, &rpc) && rpc.Code == protocol.CodeMethodNotFound {
					return fmt.Errorf("%w; the server is older than this CLI, and an admin updates it with aether server update", err)
				}
				return err
			}
			for _, entry := range stat.Entries {
				onServer[entry.Path] = entry
			}
		}
		var send []protocol.ConfigImportFile
		for i := range batch {
			item := &batch[i]
			if item.state != "" {
				continue
			}
			serverPath := configServerPath(root, item.rel)
			sum := sha256.Sum256(item.content)
			entry, exists := onServer[serverPath]
			different := fmt.Sprintf("the server has a different file (%d bytes, %d here)", entry.Size, len(item.content))
			switch {
			case !exists:
				item.state = pushCreated
			case entry.Blocked:
				item.state, item.detail = pushLeftOut, "the server has a folder, symlink, or hard link at this path"
				continue
			case entry.Revision == hex.EncodeToString(sum[:]):
				item.state, item.detail = pushSkipped, "identical on the server"
				continue
			case !opts.overwrite:
				item.state, item.detail = pushRefused, different+"; --overwrite replaces it"
				continue
			default:
				item.state = pushReplaced
				if opts.dryRun {
					item.detail = different
				}
			}
			if !opts.dryRun {
				item.sent = true
				send = append(send, protocol.ConfigImportFile{Path: serverPath, ContentBase64: base64.StdEncoding.EncodeToString(item.content), Mode: item.mode})
			}
		}
		var stopped error
		if len(send) > 0 {
			var result protocol.ConfigImportResult
			if err := c.Call(protocol.MethodConfigImport, protocol.ConfigImportParams{Harness: root.Harness, Files: send, Overwrite: opts.overwrite}, &result); err != nil {
				stopped = fmt.Errorf("config.import: %w; the %d unconfirmed files may or may not be on the server, and later files were not sent; aether config ls -r %s shows what the server has", err, len(send), root.Harness)
				for i := range batch {
					if batch[i].sent {
						batch[i].state = "unconfirmed"
					}
				}
			} else {
				if result.Error != "" {
					stopped = fmt.Errorf("upload to %s stopped: %s", root.Path, result.Error)
				}
				excluded := map[string]protocol.ConfigExcluded{}
				for _, e := range result.Excluded {
					excluded[e.Path] = e
				}
				for i := range batch {
					item := &batch[i]
					if !item.sent {
						continue
					}
					e, wasExcluded := excluded[item.rel]
					switch {
					case wasExcluded && e.Reason == "identical":
						item.state, item.detail = pushSkipped, e.Detail
					case wasExcluded && e.Reason == "differs":
						item.state, item.detail = pushRefused, e.Detail+"; --overwrite replaces it"
					case wasExcluded && e.Detail != "":
						item.state, item.detail = pushLeftOut, e.Detail
					case wasExcluded:
						item.state, item.detail = pushLeftOut, e.Reason
					case stopped != nil && !slices.Contains(result.ImportedPaths, item.rel):
						item.state = ""
					case slices.Contains(result.Replaced, item.rel):
						item.state = pushReplaced
					default:
						item.state = pushCreated
					}
				}
			}
		}
		for _, item := range batch {
			if item.state == "" {
				continue
			}
			report(item.state, item.rel, item.detail)
			if item.state == pushCreated || item.state == pushReplaced {
				uploadBytes += int64(len(item.content))
			}
		}
		batch, batchFiles, batchBytes = nil, 0, 0
		return stopped
	}
	for _, f := range files {
		content, size, err := configRead(local, f)
		if err != nil {
			return err
		}
		item := configPushItem{rel: f.rel, mode: f.mode}
		if len(content) > configMaxFileBytes {
			item.state, item.detail = pushLeftOut, configTooLarge(size)
		} else if hits := secretscan.Scan(f.rel, content); len(hits) != 0 {
			item.state, item.detail = pushLeftOut, fmt.Sprintf("secret detected at %s (%s)", hits[0].Location, hits[0].Kind)
		} else {
			if batchFiles > 0 && (batchFiles == configBatchFiles || batchBytes+len(content) > configBatchBytes) {
				if err := flush(); err != nil {
					return err
				}
			}
			item.content = content
			batchFiles++
			batchBytes += len(content)
		}
		batch = append(batch, item)
	}
	if err := flush(); err != nil {
		return err
	}
	if opts.dryRun {
		_, _ = fmt.Fprintf(out, "would create %d, replace %d, skip %d identical, refuse %d, leave out %d in %s (%d bytes to upload)\n",
			counts[pushCreated], counts[pushReplaced], counts[pushSkipped], counts[pushRefused], counts[pushLeftOut], root.Path, uploadBytes)
		return nil
	}
	_, _ = fmt.Fprintf(out, "created %d, replaced %d, skipped %d identical, refused %d, left out %d in %s (%d bytes uploaded)\n",
		counts[pushCreated], counts[pushReplaced], counts[pushSkipped], counts[pushRefused], counts[pushLeftOut], root.Path, uploadBytes)
	if counts[pushRefused] > 0 {
		return fmt.Errorf("refused %d: the server has a different file at each of those paths; read it with aether config cat %s <path>, then push with --overwrite to replace it", counts[pushRefused], root.Harness)
	}
	return nil
}
