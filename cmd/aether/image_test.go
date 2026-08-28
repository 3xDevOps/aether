package main

import (
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestWorkspaceEnvironmentDefaultsToNeutralImage(t *testing.T) {
	env := workspaceEnvironment("")
	if !env.NeutralImage || env.CustomImage != "" {
		t.Fatalf("empty image environment = %#v, want neutral selection", env)
	}
}
func TestWorkspaceInitRequiresName(t *testing.T) {
	err := workspaceInit(nil)
	if err == nil {
		t.Fatal("workspace init without a name succeeded")
	}
	if got := err.Error(); got != "usage: aether workspace init <name> [--standard | --image <image>] [--base <branch>]" {
		t.Fatalf("workspace init usage = %q", got)
	}
}

// --standard and --image both pick the workspace image, so passing both is
// a contradiction the error must spell out by naming the two flags.
func TestWorkspaceInitRejectsStandardWithImage(t *testing.T) {
	err := workspaceInit([]string{"myproject", "--standard", "--image", "busybox"})
	if err == nil {
		t.Fatal("workspace init with --standard and --image succeeded")
	}
	for _, flagName := range []string{"--standard", "--image"} {
		if !strings.Contains(err.Error(), flagName) {
			t.Errorf("error %q does not name %s", err, flagName)
		}
	}
}

func TestWorkspaceAddRejectsStandardWithImage(t *testing.T) {
	err := workspaceAdd([]string{"myproject", "--standard", "--image", "busybox"})
	if err == nil {
		t.Fatal("workspace add with --standard and --image succeeded")
	}
	for _, flagName := range []string{"--standard", "--image"} {
		if !strings.Contains(err.Error(), flagName) {
			t.Errorf("error %q does not name %s", err, flagName)
		}
	}
}

// --standard satisfies add's image requirement; only the bare form is a
// usage error.
func TestWorkspaceAddRequiresImageOrStandard(t *testing.T) {
	err := workspaceAdd([]string{"myproject"})
	if err == nil {
		t.Fatal("workspace add without an image selection succeeded")
	}
	if !strings.HasPrefix(err.Error(), "usage:") {
		t.Fatalf("workspace add error = %q, want a usage error", err)
	}
}

// --standard pins the ref the server reports, riding the same custom_image
// field the dashboard uses; the workspace never records a "standard" flag.
func TestCreateEnvironmentStandardPinsTheServerRef(t *testing.T) {
	env, err := createEnvironment(
		workspaceCreateOptions{name: "myproject", standard: true},
		protocol.ServerInfoResult{StandardImage: "ghcr.io/3xdevops/aether-standard:v1.2.3"},
	)
	if err != nil {
		t.Fatalf("createEnvironment: %v", err)
	}
	if env.CustomImage != "ghcr.io/3xdevops/aether-standard:v1.2.3" || env.NeutralImage {
		t.Fatalf("environment = %#v, want the standard ref as custom image", env)
	}
}

// A server predating server.info's standard_image cannot honor --standard;
// the error must point at the server upgrade rather than fail obscurely.
func TestCreateEnvironmentStandardNeedsAServerRef(t *testing.T) {
	_, err := createEnvironment(
		workspaceCreateOptions{name: "myproject", standard: true},
		protocol.ServerInfoResult{},
	)
	if err == nil {
		t.Fatal("createEnvironment with no standard_image succeeded")
	}
	if !strings.Contains(err.Error(), "upgrade") {
		t.Fatalf("error %q does not suggest a server upgrade", err)
	}
}

func TestCreateEnvironmentWithoutStandardUsesTheImageFlag(t *testing.T) {
	env, err := createEnvironment(
		workspaceCreateOptions{name: "myproject", image: "busybox"},
		protocol.ServerInfoResult{},
	)
	if err != nil {
		t.Fatalf("createEnvironment: %v", err)
	}
	if env.CustomImage != "busybox" || env.NeutralImage {
		t.Fatalf("environment = %#v, want the typed custom image", env)
	}
}
