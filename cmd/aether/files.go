package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "files",
		short: "read workspace and run files",
		run:   runFiles,
	})
}

func runFiles(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: aether files ls <workspace|run> [path] | aether files cat <workspace|run> <path>")
	}
	switch args[0] {
	case "ls":
		if len(args) < 2 || len(args) > 3 {
			return fmt.Errorf("usage: aether files ls <workspace|run> [path]")
		}
		path := ""
		if len(args) == 3 {
			path = args[2]
		}
		return filesList(args[1], path)
	case "cat":
		if len(args) != 3 {
			return fmt.Errorf("usage: aether files cat <workspace|run> <path>")
		}
		return filesCat(args[1], args[2])
	default:
		return fmt.Errorf("usage: aether files ls <workspace|run> [path] | aether files cat <workspace|run> <path>")
	}
}

func filesList(target, path string) error {
	return withControl(func(c *protocol.Client) error {
		params, err := filesParams(c, target, path)
		if err != nil {
			return err
		}
		var result protocol.FilesTreeResult
		if err := c.Call(protocol.MethodFilesTree, params, &result); err != nil {
			return err
		}
		for _, entry := range result.Entries {
			if entry.Kind == "dir" {
				_, _ = fmt.Fprintf(os.Stdout, "dir\t%s\n", entry.Name)
			} else {
				_, _ = fmt.Fprintf(os.Stdout, "file\t%d\t%s\n", entry.Size, entry.Name)
			}
		}
		return nil
	})
}

func filesCat(target, path string) error {
	return withControl(func(c *protocol.Client) error {
		params, err := filesParams(c, target, path)
		if err != nil {
			return err
		}
		var result protocol.FilesReadResult
		if err := c.Call(protocol.MethodFilesRead, params, &result); err != nil {
			return err
		}
		_, _ = fmt.Fprint(os.Stdout, result.Content)
		if result.Binary {
			_, _ = fmt.Fprintln(os.Stderr, "aether: binary file")
		}
		if result.Truncated {
			_, _ = fmt.Fprintln(os.Stderr, "aether: truncated at 512 KiB")
		}
		return nil
	})
}

func filesParams(c *protocol.Client, target, path string) (protocol.FilesReadParams, error) {
	var list protocol.WorkspaceListResult
	if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &list); err != nil {
		return protocol.FilesReadParams{}, err
	}
	id, err := workspaceIDIn(list.Workspaces, target)
	if err == nil {
		return protocol.FilesReadParams{WorkspaceID: id, Path: path}, nil
	}
	if !strings.HasSuffix(err.Error(), " not found") {
		return protocol.FilesReadParams{}, err
	}
	var result protocol.RunResult
	if err := c.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: target}, &result); err != nil {
		return protocol.FilesReadParams{}, err
	}
	return protocol.FilesReadParams{WorkspaceID: result.Run.WorkspaceID, RunID: result.Run.ID, Path: path}, nil
}
