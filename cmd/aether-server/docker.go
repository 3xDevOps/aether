package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
)

// Seam for tests: nothing else asks the test machine's Docker.
var dockerCheck = func(ctx context.Context) error {
	d, err := runtime.NewDocker()
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.CheckSubpathAPI(ctx)
}

const dockerInstall = "https://docs.docker.com/engine/install/"

// checkDocker reports whether the Docker daemon answers, and says what it
// means for runs when it does not. An engine too old for subpath mounts
// counts as answering: only containers that touch a shared agent account
// use them.
func checkDocker(w io.Writer) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := dockerCheck(ctx)
	switch {
	case err == nil:
		return true
	case errors.Is(err, runtime.ErrSubpathAPI):
		fix := "Upgrade Docker: " + dockerInstall
		if strings.Contains(err.Error(), "DOCKER_API_VERSION") {
			fix = "Unset DOCKER_API_VERSION, or raise it, wherever aether-server runs; upgrading Docker does not change it."
		}
		_, _ = fmt.Fprintf(w, "%v\nOnce someone shares their agent account, that person's containers and every run on the account fail on this Docker.\n%s\n\n", err, fix)
		return true
	}
	_, _ = fmt.Fprintf(w, "Docker does not answer: %v\n", err)
	_, _ = fmt.Fprintln(w, "The server runs every agent in a Docker container, so runs fail until Docker answers.")
	_, _ = fmt.Fprintf(w, "Install or start Docker: %s\n\n", dockerInstall)
	return false
}
