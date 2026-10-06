package scheduler

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

func TestDriverWithoutImplementationRefusesToStart(t *testing.T) {
	s := &Scheduler{}
	err := s.driver(domain.LaunchACP).Start(t.Context(), &supervised{runID: "run_acp"}, nil)
	if !errors.Is(err, ErrNoAgentDriver) {
		t.Fatalf("acp Start error = %v, want ErrNoAgentDriver", err)
	}
	if _, ok := s.driver(domain.LaunchHeadless).(tuiDriver); !ok {
		t.Fatal("headless runs must keep the PTY driver")
	}
}

func TestSidecarCarriesAgentSessionAndExec(t *testing.T) {
	exec := &runtime.ExecIdentity{ContainerID: "c1", ExecID: "e1", CreationKey: "k1", ClaimToken: "t1"}
	data, err := json.Marshal(sidecar{RunID: "run_1", Mode: domain.LaunchTUI, AgentSessionID: "session-1", AgentExec: exec})
	if err != nil {
		t.Fatal(err)
	}
	var decoded sidecar
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	s := &Scheduler{}
	entry := s.entryFromSidecar(&domain.Run{ID: "run_1", Mode: domain.LaunchTUI}, decoded)
	got := entry.sidecar()
	if got.AgentSessionID != "session-1" || !reflect.DeepEqual(got.AgentExec, exec) {
		t.Fatalf("recovered sidecar agent session %q exec %+v", got.AgentSessionID, got.AgentExec)
	}
}
