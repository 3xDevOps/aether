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
	"syscall"

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
	// `aether-server update` runs this with its own stdout, whose reader
	// may go away; the update ignores SIGPIPE, and so must this step of it.
	signal.Ignore(syscall.SIGPIPE)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return prepareVMHost(ctx, os.Stdout, selfupdate.DefaultChecker().BaseURL(), *o.vmKernel)
}

func vmHostSupported() error {
	if goruntime.GOARCH != "amd64" {
		return fmt.Errorf("session VMs need an x86-64 host, because releases ship the guest kernel and Cloud Hypervisor for x86-64 only; this host is %s", goruntime.GOARCH)
	}
	return nil
}

// prepareVMHost checks for KVM, then installs the tools, Cloud Hypervisor
// and guest kernel session VMs need, printing one line per step.
func prepareVMHost(ctx context.Context, w io.Writer, baseURL, kernel string) error {
	if err := vmHostSupported(); err != nil {
		return err
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
	files := []serversetup.Pinned{serversetup.CloudHypervisor}
	if kernel == server.DefaultVMKernel && selfupdate.ValidTag(version.Version) {
		k, err := serversetup.ReleaseKernel(ctx, baseURL, version.Version, kernel)
		if err != nil {
			return fmt.Errorf("guest kernel: %w", err)
		}
		files = append(files, k)
	} else if err := keepKernel(w, kernel); err != nil {
		return err
	}
	installed, missing, err := splitInstalled(files)
	if err != nil {
		return err
	}
	for _, f := range installed {
		_, _ = fmt.Fprintf(w, "%s is installed at %s\n", f.Name, f.Path)
	}
	if len(missing) == 0 {
		return nil
	}
	var what []string
	for _, f := range missing {
		what = append(what, f.Name+" at "+f.Path)
	}
	return step(w, "installing "+strings.Join(what, " and "), func() error { return serversetup.InstallAll(ctx, missing) })
}

// keepKernel accepts the kernel already at kernel. A development build has
// no release to take one from, and a vm-kernel other than the default is
// the operator's own.
func keepKernel(w io.Writer, kernel string) error {
	why := "vm-kernel names it, so it is yours to update"
	fix := "install a kernel there, or set vm-kernel back to " + server.DefaultVMKernel
	if kernel == server.DefaultVMKernel {
		why = "this development build has no release to take a kernel from"
		fix = "build one with `make kernel` and copy dist/" + serversetup.KernelAsset + " there"
	}
	if _, err := os.Stat(kernel); err != nil {
		return fmt.Errorf("guest kernel: %w; %s: %s", err, why, fix)
	}
	_, _ = fmt.Fprintf(w, "kept the guest kernel %s: %s\n", kernel, why)
	return nil
}

// splitInstalled sorts files into those already in place, whose kernel
// release it records, and those to install.
func splitInstalled(files []serversetup.Pinned) (installed, missing []serversetup.Pinned, err error) {
	for _, f := range files {
		ok, err := serversetup.Installed(f.Path, f.Asset)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			missing = append(missing, f)
			continue
		}
		if err = f.Record(); err != nil {
			return nil, nil, err
		}
		installed = append(installed, f)
	}
	return installed, missing, nil
}

// ensureVMFiles puts the Cloud Hypervisor and guest kernel this binary was
// released with in place before the server starts any VM, so an update
// from a client or the dashboard, or the rollback of one, runs VMs on the
// files the running server expects. It reaches the network only when the
// kernel in place came from another release.
func ensureVMFiles(ctx context.Context, kernel string) error {
	if vmHostSupported() != nil || os.Getenv(selfupdate.OptOutEnv) != "" {
		return nil
	}
	files := []serversetup.Pinned{serversetup.CloudHypervisor}
	if kernel == server.DefaultVMKernel && selfupdate.ValidTag(version.Version) && serversetup.ReleaseOf(kernel) != version.Version {
		k, err := serversetup.ReleaseKernel(ctx, selfupdate.DefaultChecker().BaseURL(), version.Version, kernel)
		if err != nil {
			return fmt.Errorf("guest kernel: %w", err)
		}
		files = append(files, k)
	}
	_, missing, err := splitInstalled(files)
	if err != nil {
		return err
	}
	if err = serversetup.InstallAll(ctx, missing); err != nil {
		return err
	}
	for _, f := range missing {
		slog.Info("session VMs: installed "+f.Name, "path", f.Path)
	}
	return nil
}
