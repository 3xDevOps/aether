package scheduler

import (
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestValidateTerminalImageRequiresGeneratedRunAccountPath(t *testing.T) {
	e := newTestEnv(t, nil)
	t.Setenv(fakeAgentEnv, "fake-agent {task}")
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "validate image", "fake", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	imagePath, err := e.sched.SaveTerminalImage(t.Context(), e.member.ID, run.ID, ".png", []byte("image"))
	if err != nil {
		t.Fatalf("SaveTerminalImage: %v", err)
	}
	if err := e.sched.ValidateTerminalImage(t.Context(), run.ID, imagePath); err != nil {
		t.Fatalf("ValidateTerminalImage(valid): %v", err)
	}
	for _, reference := range []string{
		filepath.Join(filepath.Dir(imagePath), "..", "secret.png"),
		filepath.Join(filepath.Dir(imagePath), "arbitrary.png"),
		"/tmp/terminal-image.png",
		imagePath + "\n",
	} {
		if err := e.sched.ValidateTerminalImage(t.Context(), run.ID, reference); err == nil {
			t.Errorf("ValidateTerminalImage(%q) accepted an untrusted reference", reference)
		}
	}
}
