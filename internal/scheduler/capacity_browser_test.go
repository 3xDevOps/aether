package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

type browserAdmissionRuntime struct {
	*admissionRuntime
	creates int
}

func (r *browserAdmissionRuntime) CreateBrowser(context.Context, runtime.BrowserSpec) (runtime.ID, error) {
	r.creates++
	return "", errors.New("browser create failed")
}

func (r *browserAdmissionRuntime) InspectBrowser(context.Context, runtime.ID) (runtime.BrowserInfo, error) {
	return runtime.BrowserInfo{}, runtime.ErrNotFound
}

func TestBrowserProvisioningCapacityAndNativeErrorMapping(t *testing.T) {
	var browserRT *browserAdmissionRuntime
	e, rt := newAdmissionEnv(t, func(cfg *Config) {
		browserRT = &browserAdmissionRuntime{admissionRuntime: cfg.Runtime.(*admissionRuntime)}
		cfg.Runtime = browserRT
		cfg.BrowserImage = "aether/browser:test"
		cfg.Control = control.New(control.Config{})
	})
	run, _ := e.launchFake(t, "browser capacity")
	live, err := e.sched.ResolveLiveRun(t.Context(), run.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	host := healthyAdmissionHost()
	host.MemoryAvailableBytes = 1
	rt.setHost(host)
	if _, _, lookupErr := e.sched.browserClient(t.Context(), live, false); !errors.Is(lookupErr, os.ErrNotExist) {
		t.Fatalf("browser lookup charged new provisioning: %v", lookupErr)
	}
	if _, _, createErr := e.sched.browserClient(t.Context(), live, true); !errors.Is(createErr, ErrMemoryPressure) {
		t.Fatalf("browser create under memory pressure: %v", createErr)
	}
	d := e.sched.developmentState()
	if d.reserved[run.ID] || browserRT.creates != 0 || e.sched.capacityReservations != 0 {
		t.Fatal("refused browser provisioning consumed a reservation or reached runtime Create")
	}
	raw, err := json.Marshal(map[string]any{
		"control_session_id": "browser-capacity-test", "url": "https://example.com", "width": 800, "height": 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.sched.HandleAgent(t.Context(), run.ID, protocol.MethodDevBrowserOpen, raw)
	var wire *protocol.Error
	if !errors.As(err, &wire) || wire.Code != protocol.CodeUnavailable || !strings.Contains(wire.Message, "memory") {
		t.Fatalf("native browser RPC lost capacity reason: %v", err)
	}
	// Any failed browser provisioning (including host ownership setup before
	// Create) releases the shared allowance. Browser lifecycle's uncertain
	// creation identity remains separate and is never silently replayed.
	host.MemoryAvailableBytes = host.MemoryTotalBytes/10 + startupMemoryBytes
	rt.setHost(host)
	if _, _, createErr := e.sched.browserClient(t.Context(), live, true); createErr == nil {
		t.Fatal("failing browser runtime unexpectedly succeeded")
	}
	release, err := e.sched.reserveCapacity(t.Context())
	if err != nil {
		t.Fatalf("browser error leaked shared capacity reservation: %v", err)
	}
	release()
}
