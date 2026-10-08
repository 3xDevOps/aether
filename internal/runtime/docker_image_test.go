package runtime

import (
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
		_, _ = w.Write([]byte(`{"Config":{"Env":["npm_config_cache=/custom/npm","PIP_CACHE_DIR=","UV_CACHE_DIR=/custom/uv","GOCACHE=/custom/go-build","GOMODCACHE=/custom/modules","HOME=/custom/home","SECRET_TOKEN=do-not-propagate"]}}`))
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
	want := map[string]string{"npm_config_cache": "/custom/npm", "PIP_CACHE_DIR": "", "UV_CACHE_DIR": "/custom/uv", "GOCACHE": "/custom/go-build", "GOMODCACHE": "/custom/modules"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("image cache settings = %#v, want only explicit tool settings", got)
	}
}
