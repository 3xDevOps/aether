package sshd

import "testing"

func TestValidateExtraWorkspaces(t *testing.T) {
	t.Parallel()
	if err := validateExtraWorkspaces("a", []string{"b", "c"}); err != nil {
		t.Fatalf("valid extras: %v", err)
	}
	for name, extras := range map[string][]string{
		"primary repeated": {"a"},
		"duplicate":        {"b", "b"},
		"empty":            {""},
	} {
		if err := validateExtraWorkspaces("a", extras); err == nil {
			t.Errorf("%s: accepted %v", name, extras)
		}
	}
}
