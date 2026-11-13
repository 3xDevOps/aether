package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/server"
	"github.com/3xDevOps/Aether/internal/serversetup"
	"github.com/3xDevOps/Aether/internal/version"
)

const vmUsage = "usage: aether-server vm install [--config <file>]"

// kvmDevice is a seam for install_test.go: no test host is promised KVM.
var kvmDevice = "/dev/kvm"

func vmCmd(args []string) error {
	if len(args) == 0 || args[0] != "install" {
		return errors.New(vmUsage)
	}
	o, _, rest, err := loadOptions("vm install", args[1:], nil)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New(vmUsage)
	}
	if os.Geteuid() != 0 {
		return errors.New("installing what session VMs need takes root; re-run with sudo")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return prepareVMHost(ctx, os.Stdout, selfupdate.DefaultChecker().BaseURL(), *o.vmKernel)
}

// prepareVMHost checks for KVM, then installs the tools, Cloud Hypervisor
// and guest kernel session VMs need, printing one line per step.
func prepareVMHost(ctx context.Context, w io.Writer, baseURL, kernel string) error {
	if goruntime.GOARCH != "amd64" {
		return fmt.Errorf("session VMs need an x86-64 host, because releases ship the guest kernel for x86-64 only; this host is %s", goruntime.GOARCH)
	}
	if err := step(w, "checking "+kvmDevice, func() error { return serversetup.CheckKVM(kvmDevice) }); err != nil {
		return err
	}
	if missing := serversetup.MissingVMTools(); len(missing) > 0 {
		var packages []string
		for _, tool := range missing {
			packages = append(packages, tool.Package)
		}
		if err := step(w, "installing "+strings.Join(packages, " ")+" with apt-get", func() error {
			return serversetup.InstallPackages(ctx, packages)
		}); err != nil {
			return err
		}
		if still := serversetup.MissingVMTools(); len(still) > 0 {
			return fmt.Errorf("apt-get installed %s, and %s is still not on PATH", still[0].Package, still[0].Program)
		}
	}
	kernelFile, err := releaseKernel(ctx, w, baseURL, kernel)
	if err != nil {
		return err
	}
	for _, f := range append([]serversetup.Pinned{serversetup.CloudHypervisor}, kernelFile...) {
		installed, err := serversetup.Installed(f.Path, f.Asset)
		if err != nil {
			return err
		}
		if installed {
			_, _ = fmt.Fprintf(w, "%s is installed at %s\n", f.Name, f.Path)
			continue
		}
		if err := step(w, "installing "+f.Name+" at "+f.Path, func() error { return f.Install(ctx) }); err != nil {
			return err
		}
	}
	return nil
}

// releaseKernel is this binary's release kernel, to install at kernel. A
// development build has no release to take one from, and a vm-kernel other
// than the default is the operator's own: both keep the kernel that is
// there, return none, and say so on w.
func releaseKernel(ctx context.Context, w io.Writer, baseURL, kernel string) ([]serversetup.Pinned, error) {
	if kernel == server.DefaultVMKernel && selfupdate.ValidTag(version.Version) {
		k, err := serversetup.ReleaseKernel(ctx, baseURL, version.Version, kernel)
		if err != nil {
			return nil, fmt.Errorf("guest kernel: %w", err)
		}
		return []serversetup.Pinned{k}, nil
	}
	why := "vm-kernel names it, so it is yours to update"
	fix := "install a kernel there, or set vm-kernel back to " + server.DefaultVMKernel
	if kernel == server.DefaultVMKernel {
		why = "this development build has no release to take a kernel from"
		fix = "build one with `make kernel` and copy dist/" + serversetup.KernelAsset + " there"
	}
	if _, err := os.Stat(kernel); err != nil {
		return nil, fmt.Errorf("guest kernel: %w; %s: %s", err, why, fix)
	}
	_, _ = fmt.Fprintf(w, "kept the guest kernel %s: %s\n", kernel, why)
	return nil, nil
}

// keepVMFilesCurrent puts the Cloud Hypervisor and guest kernel this binary
// was released with in place when its unit starts it, so an update from the
// dashboard, or the rollback of a failed one, leaves the files the running
// server expects.
func keepVMFilesCurrent(kernel string) {
	if os.Getenv("INVOCATION_ID") == "" || os.Getenv(selfupdate.OptOutEnv) != "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	files := []serversetup.Pinned{serversetup.CloudHypervisor}
	if k, err := releaseKernel(ctx, io.Discard, selfupdate.DefaultChecker().BaseURL(), kernel); err != nil {
		slog.Warn("session VMs: could not check the guest kernel; `sudo aether-server vm install` retries", "error", err)
	} else {
		files = append(files, k...)
	}
	for _, f := range files {
		installed, err := serversetup.Installed(f.Path, f.Asset)
		if err == nil && installed {
			continue
		}
		if err == nil {
			err = f.Install(ctx)
		}
		if err != nil {
			slog.Warn("session VMs: could not install "+f.Name+"; `sudo aether-server vm install` retries", "path", f.Path, "error", err)
			continue
		}
		slog.Info("session VMs: installed "+f.Name, "path", f.Path)
	}
	if missing := serversetup.MissingVMTools(); len(missing) > 0 {
		slog.Warn("session VMs: "+missing[0].Program+" is missing; install it with `sudo aether-server vm install`", "package", missing[0].Package)
	}
}
