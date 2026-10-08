package scheduler

import (
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

var onePixelPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9c, 0x63, 0xf8, 0xcf, 0xc0, 0xf0,
	0x1f, 0x00, 0x05, 0x00, 0x01, 0xff, 0x89, 0x99,
	0x3d, 0x1d, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45,
	0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

func TestValidateTerminalImageRequiresGeneratedRunAccountPath(t *testing.T) {
	e := newTestEnv(t, nil)
	t.Setenv(fakeAgentEnv, "fake-agent {task}")
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "validate image", "fake", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	imagePath, err := e.sched.SaveTerminalImage(t.Context(), e.member.ID, run.ID, ".png", onePixelPNG)
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
