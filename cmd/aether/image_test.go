package main

import (
	"testing"
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
	if got := err.Error(); got != "usage: aether workspace init <name> [--image <image>]" {
		t.Fatalf("workspace init usage = %q", got)
	}
}
