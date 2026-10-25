package runtime

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

func TestCheckNetwork(t *testing.T) {
	isolated := map[string]string{iccOption: "false"}
	for _, tc := range []struct {
		name     string
		net      network.Network
		internal bool
		ok       bool
	}{
		{"isolated bridge", network.Network{Driver: "bridge", Options: isolated}, false, true},
		{"operator MTU", network.Network{Driver: "bridge", Options: map[string]string{iccOption: "false", "com.docker.network.driver.mtu": "1400"}}, false, true},
		{"icc on", network.Network{Driver: "bridge", Options: map[string]string{iccOption: "true"}}, false, false},
		{"icc unset", network.Network{Driver: "bridge"}, false, false},
		{"macvlan", network.Network{Driver: "macvlan", Options: isolated}, false, false},
		{"internal bridge", network.Network{Driver: "bridge", Options: isolated, Internal: true}, true, true},
		{"bridge with a route out", network.Network{Driver: "bridge", Options: isolated}, true, false},
		{"internal with icc on", network.Network{Driver: "bridge", Internal: true}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkNetwork("aether-test", tc.net, tc.internal)
			if tc.ok {
				if err != nil {
					t.Fatalf("checkNetwork = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "`docker network rm aether-test`") {
				t.Fatalf("checkNetwork = %v, want the docker network rm fix", err)
			}
		})
	}
}

// fakeNetworkAPI holds at most one network named AgentNetwork. A set
// raceWinner is what a concurrent create left behind: the next network create
// answers 409 and the winner appears.
type fakeNetworkAPI struct {
	mu         sync.Mutex
	current    *network.Network
	raceWinner *network.Network
	creates    []network.CreateRequest
	containers int
}

func (f *fakeNetworkAPI) serve(t *testing.T) *Docker {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/_ping":
			w.Header().Set("Api-Version", "1.52")
			_, _ = io.WriteString(w, "OK")
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/networks/"+AgentNetwork):
			if f.current == nil {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"message":"network `+AgentNetwork+` not found"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(f.current)
		case strings.HasSuffix(r.URL.Path, "/networks/create"):
			var req network.CreateRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.creates = append(f.creates, req)
			if f.raceWinner != nil {
				f.current, f.raceWinner = f.raceWinner, nil
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"message":"network with name `+AgentNetwork+` already exists"}`)
				return
			}
			f.current = &network.Network{Name: req.Name, Driver: req.Driver, Options: req.Options}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Id":"n1"}`)
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			f.containers++
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Id":"c1","Warnings":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	cli, err := client.New(client.WithHost("tcp://" + srv.Listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return &Docker{cli: cli, waitClient: cli, namePrefix: defaultNamePrefix, networkMode: AgentNetwork}
}

func TestCreateEnsuresAgentNetwork(t *testing.T) {
	isolated := &network.Network{Name: AgentNetwork, Driver: "bridge", Options: map[string]string{iccOption: "false"}}
	open := &network.Network{Name: AgentNetwork, Driver: "bridge", Options: map[string]string{iccOption: "true"}}
	for _, tc := range []struct {
		name                string
		current, raceWinner *network.Network
		refused             bool
	}{
		{name: "first use creates it"},
		{name: "existing isolated network", current: isolated},
		{name: "existing open network", current: open, refused: true},
		{name: "lost the create race to an isolated network", raceWinner: isolated},
		{name: "lost the create race to an open network", raceWinner: open, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeNetworkAPI{current: tc.current, raceWinner: tc.raceWinner}
			d := api.serve(t)
			_, err := d.Create(t.Context(), validSpec())
			api.mu.Lock()
			defer api.mu.Unlock()
			if tc.refused {
				if err == nil || !strings.Contains(err.Error(), "`docker network rm "+AgentNetwork+"`") || api.containers != 0 {
					t.Fatalf("Create = %v with %d containers; want the docker network rm fix and none", err, api.containers)
				}
				return
			}
			if err != nil || api.containers != 1 {
				t.Fatalf("Create = %v with %d containers; want one", err, api.containers)
			}
			if tc.current == nil {
				if len(api.creates) != 1 || api.creates[0].Driver != "bridge" || api.creates[0].Options[iccOption] != "false" {
					t.Fatalf("network creates = %+v, want one bridge with %s=false", api.creates, iccOption)
				}
			} else if len(api.creates) != 0 {
				t.Fatalf("network creates = %+v, want none", api.creates)
			}
		})
	}
}

func TestCreateRecreatesRemovedAgentNetwork(t *testing.T) {
	api := &fakeNetworkAPI{}
	d := api.serve(t)
	for range 2 {
		if _, err := d.Create(t.Context(), validSpec()); err != nil {
			t.Fatalf("Create = %v", err)
		}
		api.mu.Lock()
		api.current = nil
		api.mu.Unlock()
	}
	if len(api.creates) != 2 || api.containers != 2 {
		t.Fatalf("%d network creates for %d containers, want 2 each", len(api.creates), api.containers)
	}
}
