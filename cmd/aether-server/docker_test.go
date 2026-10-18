package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/runtime"
)

const dockerDown = "runtime: read docker engine API version: Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"

func fakeDocker(t *testing.T, err error) {
	t.Helper()
	old := dockerCheck
	t.Cleanup(func() { dockerCheck = old })
	dockerCheck = func(context.Context) error { return err }
}

func TestCheckDocker(t *testing.T) {
	old := fmt.Errorf(`runtime: docker engine API "1.44" %w; that needs API 1.45 (Docker Engine 26.0) or newer`, runtime.ErrSubpathAPI)
	pinned := fmt.Errorf(`runtime: docker client sends API "1.44" (DOCKER_API_VERSION), which %w; that needs API 1.45 or newer`, runtime.ErrSubpathAPI)
	for name, tc := range map[string]struct {
		err    error
		answer bool
		want   string
	}{
		"answers": {nil, true, ""},
		"down": {errors.New(dockerDown), false, "Docker does not answer: " + dockerDown + "\n" +
			"The server runs every agent in a Docker container, so runs fail until Docker answers.\n" +
			"Install or start Docker: https://docs.docker.com/engine/install/\n\n"},
		"too old for subpath mounts": {old, true, old.Error() + "\n" +
			"Once someone shares their agent account, that person's containers and every run on the account fail on this Docker.\n" +
			"Upgrade Docker: https://docs.docker.com/engine/install/\n\n"},
		"client pinned below subpath mounts": {pinned, true, pinned.Error() + "\n" +
			"Once someone shares their agent account, that person's containers and every run on the account fail on this Docker.\n" +
			"Unset DOCKER_API_VERSION, or raise it, wherever aether-server runs; upgrading Docker does not change it.\n\n"},
	} {
		t.Run(name, func(t *testing.T) {
			fakeDocker(t, tc.err)
			var out bytes.Buffer
			if got := checkDocker(&out); got != tc.answer || out.String() != tc.want {
				t.Errorf("checkDocker = %v, output %q; want %v, %q", got, out.String(), tc.answer, tc.want)
			}
		})
	}
}

// nextSteps is the next: block at the end of out.
func nextSteps(t *testing.T, out string) string {
	t.Helper()
	at := strings.LastIndex(out, "\nnext:\n")
	if at < 0 {
		t.Fatalf("no next: block in:\n%s", out)
	}
	return out[at+len("\nnext:\n"):]
}
