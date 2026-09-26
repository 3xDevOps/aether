package main

import (
	"strings"
	"testing"
)

func TestWorkspaceDeleteRequiresExplicitConfirmation(t *testing.T) {
	for _, args := range [][]string{{"project"}, {"project", "--yes=false"}} {
		if err := workspaceDelete(args); err == nil || !strings.Contains(err.Error(), "--yes is required") {
			t.Fatalf("workspace delete %v = %v; must refuse before connecting without explicit confirmation", args, err)
		}
	}
	if err := workspaceDelete([]string{"--yes"}); err == nil || !strings.Contains(err.Error(), "<name-or-id>") {
		t.Fatalf("workspace delete without an explicit target = %v", err)
	}
}
