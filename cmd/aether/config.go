package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	profilesvc "github.com/3xDevOps/Aether/internal/profile"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/secretscan"
)

func init() {
	register(command{
		name:  "config",
		short: "list and upload agent configuration",
		run:   runConfig,
	})
}

const configUsage = "usage: aether config ls\n" +
	"   or: aether config ls <agent> [path]\n" +
	"   or: aether config push [--dry-run] <agent> <path>...\n" +
	"each path is a file or directory inside the agent's directory, relative to it; . uploads the whole directory"

const (
	// configMaxFileBytes mirrors memberhome.ConfigMaxFileBytes, which the
	// server enforces per file and per config.import request.
	configMaxFileBytes = 64 << 20
	configBatchFiles   = 2000
	configBatchBytes   = 20 << 20
)

func runConfig(args []string) error {
	if len(args) == 0 {
		return errors.New(configUsage)
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Println(configUsage)
		return nil
	case "ls":
		switch len(args) {
		case 1:
			return withControl(func(c *protocol.Client) error { return configList(c, os.Stdout) })
		case 2, 3:
			path := ""
			if len(args) == 3 {
				path = args[2]
			}
			return withControl(func(c *protocol.Client) error { return configTree(c, os.Stdout, args[1], path) })
		}
		return errors.New(configUsage)
	case "push":
		var dryRun bool
		var positional []string
		for _, arg := range args[1:] {
			switch {
			case arg == "--dry-run" || arg == "-dry-run":
				dryRun = true
			case strings.HasPrefix(arg, "-"):
				return fmt.Errorf("unknown flag %s\n%s", arg, configUsage)
			default:
				positional = append(positional, arg)
			}
		}
		if len(positional) < 2 {
			return errors.New(configUsage)
		}
		return withControl(func(c *protocol.Client) error {
			return configPush(c, os.Stdout, positional[0], positional[1:], dryRun)
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
		for _, name := range append(root.CredentialNames, "*.pem") {
			if !seen[name] {
				seen[name] = true
				credentials = append(credentials, name)
			}
		}
		runtimePaths := "none"
		if len(root.RuntimeIgnores) > 0 {
			runtimePaths = strings.Join(root.RuntimeIgnores, ", ")
		}
		_, _ = fmt.Fprintf(out, "%s (%s): %s, %s\n", root.Harness, root.DisplayName, root.Path, here)
		_, _ = fmt.Fprintf(out, "  always left out, credentials: %s\n", strings.Join(credentials, ", "))
		_, _ = fmt.Fprintf(out, "  always left out, runtime and history: %s\n", runtimePaths)
	}
	_, _ = fmt.Fprintln(out, "always left out, every agent: symlinks, special files, files over 64 MiB, .aether-profile-ignore, and files the secret scanner flags")
	return nil
}

func configTree(c *protocol.Client, out io.Writer, agent, path string) error {
	root, err := configRoot(c, agent)
	if err != nil {
		return err
	}
	if path != "" {
		path = configServerPath(root, path)
	}
	var result protocol.ConfigTreeResult
	if err := c.Call(protocol.MethodConfigTree, protocol.ConfigTreeParams{Harness: root.Harness, Path: path}, &result); err != nil {
		return err
	}
	for _, entry := range result.Entries {
		if entry.Kind == "dir" {
			_, _ = fmt.Fprintf(out, "dir\t%s\n", entry.Name)
		} else {
			_, _ = fmt.Fprintf(out, "file\t%d\t%s\n", entry.Size, entry.Name)
		}
	}
	return nil
}

// configServerPath prefixes rel with the agent's directory relative to home.
// The server strips one such prefix, so a rel that starts with the
// directory's own name, such as .claude/settings.json, would otherwise land
// one level up.
func configServerPath(root protocol.ConfigRoot, rel string) string {
	return strings.TrimPrefix(root.Path, "~/") + "/" + rel
}

// configFile is a file the walk will read; rel is slash-separated and
// relative to the agent's directory.
type configFile struct {
	rel  string
	mode uint32
}

type configLeftOut struct {
	rel, reason string
}

// walkConfig resolves each path argument against the agent's directory, dir,
// whose symlinks resolve to resolved, and classifies everything under it
// without reading file contents.
func walkConfig(root protocol.ConfigRoot, dir, resolved string, paths []string) ([]configFile, []configLeftOut, error) {
	var files []configFile
	var leftOut []configLeftOut
	seen := map[string]bool{}
	starts := make([]string, 0, len(paths))
args:
	for _, arg := range paths {
		abs := arg
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(dir, abs)
		}
		rel, err := filepath.Rel(dir, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, nil, fmt.Errorf("%s is outside %s", arg, root.Path)
		}
		var component string
		for _, name := range strings.Split(rel, string(filepath.Separator)) {
			component = filepath.Join(component, name)
			info, err := os.Lstat(filepath.Join(resolved, component))
			if err != nil {
				return nil, nil, err
			}
			if info.Mode()&fs.ModeSymlink != 0 {
				if slash := filepath.ToSlash(rel); !seen[slash] {
					seen[slash] = true
					leftOut = append(leftOut, configLeftOut{rel: slash, reason: fmt.Sprintf("symlink %s, not followed", filepath.ToSlash(component))})
				}
				continue args
			}
		}
		starts = append(starts, rel)
	}
	for _, start := range starts {
		err := filepath.WalkDir(filepath.Join(resolved, start), func(p string, d fs.DirEntry, err error) error {
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
			reason := ""
			switch {
			case profilesvc.DeniedBasename(rel, root.CredentialNames):
				reason = "credential"
			case rel == ".aether-profile-ignore":
				reason = "Aether profile ignore file"
			case configRuntimeIgnored(root.RuntimeIgnores, rel):
				reason = "runtime or history path"
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
			mode := uint32(info.Mode().Perm())
			if runtime.GOOS == "windows" {
				mode = 0
			}
			files = append(files, configFile{rel: rel, mode: mode})
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

// configRead reads through local, which refuses a file swapped for a symlink
// out of the agent's directory after the walk, and stops one byte past the
// upload limit in case the file grew after the walk. size is the file's size
// when it was read.
func configRead(local *os.Root, rel string) (content []byte, size int64, err error) {
	f, err := local.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()
	content, err = io.ReadAll(io.LimitReader(f, configMaxFileBytes+1))
	if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	return content, info.Size(), nil
}

// configRuntimeIgnored matches the server's runtime ignores: exact,
// case-sensitive, root-relative component prefixes.
func configRuntimeIgnored(ignores []string, rel string) bool {
	for _, pattern := range ignores {
		ignored := strings.TrimSuffix(pattern, "/")
		if rel == ignored || strings.HasPrefix(rel, ignored+"/") {
			return true
		}
	}
	return false
}

func configPush(c *protocol.Client, out io.Writer, agent string, paths []string, dryRun bool) error {
	root, err := configRoot(c, agent)
	if err != nil {
		return err
	}
	dir, err := configLocalDir(root)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	files, leftOut, err := walkConfig(root, dir, resolved, paths)
	if err != nil {
		return err
	}
	local, err := os.OpenRoot(resolved)
	if err != nil {
		return err
	}
	defer func() { _ = local.Close() }()
	leave := "left out"
	if dryRun {
		leave = "would leave out"
	}
	for _, l := range leftOut {
		_, _ = fmt.Fprintf(out, "%s %s: %s\n", leave, l.rel, l.reason)
	}
	skipped := len(leftOut)
	var uploaded int
	var uploadedBytes int64
	var batch []protocol.ConfigImportFile
	var batchRels []string
	var batchBytes int
	send := func() error {
		var result protocol.ConfigImportResult
		if err := c.Call(protocol.MethodConfigImport, protocol.ConfigImportParams{Harness: root.Harness, Files: batch}, &result); err != nil {
			for _, rel := range batchRels {
				_, _ = fmt.Fprintf(out, "unconfirmed %s\n", rel)
			}
			return fmt.Errorf("config.import: %w; the %d unconfirmed files may or may not be on the server, and later files were not sent; aether config ls %s shows what the server has", err, len(batch), root.Harness)
		}
		excluded := map[string]bool{}
		for _, e := range result.Excluded {
			excluded[e.Path] = true
			reason := e.Detail
			if reason == "" {
				reason = e.Reason
			}
			_, _ = fmt.Fprintf(out, "left out %s: %s\n", e.Path, reason)
		}
		skipped += len(result.Excluded)
		if result.Error != "" {
			for _, p := range result.ImportedPaths {
				_, _ = fmt.Fprintf(out, "uploaded %s\n", p)
			}
			return fmt.Errorf("upload to %s stopped: %s", root.Path, result.Error)
		}
		for _, rel := range batchRels {
			if !excluded[rel] {
				_, _ = fmt.Fprintf(out, "uploaded %s\n", rel)
			}
		}
		uploaded += result.Files
		uploadedBytes += result.Bytes
		batch, batchRels, batchBytes = nil, nil, 0
		return nil
	}
	for _, f := range files {
		content, size, err := configRead(local, f.rel)
		if err != nil {
			return err
		}
		if len(content) > configMaxFileBytes {
			_, _ = fmt.Fprintf(out, "%s %s: %s\n", leave, f.rel, configTooLarge(size))
			skipped++
			continue
		}
		if hits := secretscan.Scan(f.rel, content); len(hits) != 0 {
			_, _ = fmt.Fprintf(out, "%s %s: secret detected at %s (%s)\n", leave, f.rel, hits[0].Location, hits[0].Kind)
			skipped++
			continue
		}
		if dryRun {
			_, _ = fmt.Fprintf(out, "would upload %s\n", f.rel)
			uploaded++
			uploadedBytes += int64(len(content))
			continue
		}
		if len(batch) > 0 && (len(batch) == configBatchFiles || batchBytes+len(content) > configBatchBytes) {
			if err := send(); err != nil {
				return err
			}
		}
		batch = append(batch, protocol.ConfigImportFile{Path: configServerPath(root, f.rel), ContentBase64: base64.StdEncoding.EncodeToString(content), Mode: f.mode})
		batchRels = append(batchRels, f.rel)
		batchBytes += len(content)
	}
	if len(batch) > 0 {
		if err := send(); err != nil {
			return err
		}
	}
	noun := "files"
	if uploaded == 1 {
		noun = "file"
	}
	if dryRun {
		_, _ = fmt.Fprintf(out, "would upload %d %s (%d bytes) to %s; would leave out %d\n", uploaded, noun, uploadedBytes, root.Path, skipped)
	} else {
		_, _ = fmt.Fprintf(out, "uploaded %d %s (%d bytes) to %s; left out %d\n", uploaded, noun, uploadedBytes, root.Path, skipped)
	}
	return nil
}
