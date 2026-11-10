package runtime

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

func TestImageCacheEnvironmentKeepsOnlyExplicitToolSettings(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/images/saved:latest/json") {
			t.Errorf("unexpected image request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Config":{"Env":["npm_config_cache=/custom/npm","PIP_CACHE_DIR=","UV_CACHE_DIR=/custom/uv","GOCACHE=/custom/go-build","GOMODCACHE=/custom/modules","NPM_CONFIG_PREFIX=/opt/npm","CARGO_INSTALL_ROOT=/opt/cargo","HOME=/custom/home","SECRET_TOKEN=do-not-propagate"]}}`))
	}))
	defer api.Close()
	cli, err := client.New(client.WithHost("tcp://"+api.Listener.Addr().String()), client.WithAPIVersion("1.51"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()
	d := &Docker{cli: cli}
	got, err := d.ImageCacheEnvironment(t.Context(), "saved:latest")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"npm_config_cache": "/custom/npm", "PIP_CACHE_DIR": "", "UV_CACHE_DIR": "/custom/uv", "GOCACHE": "/custom/go-build", "GOMODCACHE": "/custom/modules", "NPM_CONFIG_PREFIX": "/opt/npm", "CARGO_INSTALL_ROOT": "/opt/cargo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("image cache settings = %#v, want only explicit tool settings", got)
	}
}

func TestExportImageReadsAContainerItRemovesOnClose(t *testing.T) {
	var removed bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/create"):
			var body struct {
				Image  string
				Labels map[string]string
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if label, ok := body.Labels[labelCreationKey]; body.Image != "aether/member-m:new" || !ok || label != "" {
				t.Errorf("create = %+v, want the image with an empty creation key", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"check"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/check/export"):
			_, _ = w.Write([]byte("tar bytes"))
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/check"):
			removed = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/images/json"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"Created":10,"RepoTags":["aether/member-m:old"]},{"Created":30,"RepoTags":["aether/member-m:new"]},{"Created":20,"RepoTags":["aether/member-m:mid","other:latest"]}]`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer api.Close()
	cli, err := client.New(client.WithHost("tcp://"+api.Listener.Addr().String()), client.WithAPIVersion("1.51"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()
	d := &Docker{cli: cli}
	tags, err := d.ListImageTags(t.Context(), "aether/member-m")
	if want := []string{"aether/member-m:new", "aether/member-m:mid", "aether/member-m:old"}; err != nil || !reflect.DeepEqual(tags, want) {
		t.Fatalf("ListImageTags = %v, %v, want %v", tags, err, want)
	}
	export, err := d.ExportImage(t.Context(), "aether/member-m:new")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(export)
	if err != nil || string(data) != "tar bytes" {
		t.Fatalf("export = %q, %v", data, err)
	}
	if removed {
		t.Fatal("container removed before the export was closed")
	}
	if err := export.Close(); err != nil || !removed {
		t.Fatalf("Close = %v, removed = %v, want the container removed", err, removed)
	}
}
