package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"

	"github.com/3xDevOps/Aether/internal/dictation"
	containerruntime "github.com/3xDevOps/Aether/internal/runtime"
)

const dictationUsage = "usage: aether-server dictation <install [--gpu]|status|remove> [--config <file>]"

func dictationCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(dictationUsage)
	}
	sub := args[0]
	if sub == "engine" {
		os.Exit(dictation.RunEngine(args[1:]))
	}
	var gpu *bool
	var extra func(*flag.FlagSet)
	if sub == "install" {
		extra = func(fs *flag.FlagSet) {
			gpu = fs.Bool("gpu", false, "run the engine on an NVIDIA GPU with CUDA 12 and cuDNN 9 (linux/amd64)")
		}
	}
	o, _, rest, err := loadOptions("dictation "+sub, args[1:], extra)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New(dictationUsage)
	}
	dir := dictation.Dir(*o.dataDir)
	switch sub {
	case "install":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		docker, err := containerruntime.NewDocker()
		if err != nil {
			return err
		}
		defer func() { _ = docker.Close() }()
		return dictation.Install(ctx, dir, runtime.GOARCH, *gpu, docker, os.Stdout)
	case "status":
		m, err := dictation.ReadManifest(dir)
		if err != nil {
			return err
		}
		fmt.Printf("installed in %s: %s, %s on %s in %s\n", dir, m.Model, m.Runtime, m.Provider, m.Image)
		return nil
	case "remove":
		if err := dictation.Remove(dir); err != nil {
			return err
		}
		fmt.Printf("removed the install in %s\n", dir)
		return nil
	}
	return fmt.Errorf("unknown dictation command %q (want install, status or remove)", sub)
}
