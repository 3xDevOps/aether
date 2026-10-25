//go:build integration

package runtime

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

// Two containers on AgentNetwork cannot open each other's listening port,
// while the host still reaches each of them, as `aether forward` does, and
// each of them still reaches the host.
func TestDockerAgentNetworkIsolatesContainers(t *testing.T) {
	d, err := NewDocker(WithLabels(map[string]string{"aether.test": t.Name()}))
	if err != nil {
		t.Fatalf("NewDocker() error: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := t.Context()
	serve := Spec{Image: testImage, Command: []string{"/bin/sh", "-c", "mkdir -p /tmp/www && echo run > /tmp/www/index.html && exec httpd -f -p 8080 -h /tmp/www"}}
	ids := []ID{createContainer(t, d, serve), createContainer(t, d, serve)}
	ips := make([]string, len(ids))
	for i, id := range ids {
		if err = d.Start(ctx, id); err != nil {
			t.Fatalf("Start() error: %v", err)
		}
		if ips[i], err = d.ContainerIP(ctx, id); err != nil {
			t.Fatalf("ContainerIP() error: %v", err)
		}
		waitHostGet(t, "http://"+net.JoinHostPort(ips[i], "8080")+"/", "run")
	}

	info, err := d.cli.ContainerInspect(ctx, string(ids[0]), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("ContainerInspect() error: %v", err)
	}
	endpoint := info.Container.NetworkSettings.Networks[AgentNetwork]
	if endpoint == nil || endpoint.IPAddress.String() != ips[0] {
		t.Fatalf("ContainerIP() = %s, want the address on %s; networks: %+v", ips[0], AgentNetwork, info.Container.NetworkSettings.Networks)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(endpoint.Gateway.String(), "0"))
	if err != nil {
		t.Fatalf("listen on the %s gateway: %v", AgentNetwork, err)
	}
	host := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "host") }),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = host.Serve(ln) }()
	t.Cleanup(func() { _ = host.Close() })

	for i, id := range ids {
		peer := "http://" + net.JoinHostPort(ips[1-i], "8080") + "/"
		if code, out, _ := containerGet(t, d, id, peer); code == 0 {
			t.Fatalf("container %d fetched %s from its peer: %q", i, peer, out)
		}
		hostURL := "http://" + ln.Addr().String() + "/"
		if code, out, errOut := containerGet(t, d, id, hostURL); code != 0 || out != "host" {
			t.Fatalf("container %d fetching %s: exit %d, stdout %q, stderr %q", i, hostURL, code, out, errOut)
		}
	}
}

// containerGet's timeout bounds a connect whose SYN is dropped.
func containerGet(t *testing.T, d *Docker, id ID, url string) (int, string, string) {
	t.Helper()
	code, stdout, stderr, err := d.Exec(t.Context(), id, []string{"timeout", "10", "wget", "-q", "-T", "3", "-O", "-", url}, "")
	if err != nil {
		t.Fatalf("Exec(wget %s) error: %v", url, err)
	}
	return code, strings.TrimSpace(stdout), strings.TrimSpace(stderr)
}

func waitHostGet(t *testing.T, url, want string) {
	t.Helper()
	httpClient := http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := httpClient.Get(url)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if strings.TrimSpace(string(body)) == want {
				return
			}
			err = fmt.Errorf("status %s, body %q", resp.Status, body)
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s from the host: %v", url, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
