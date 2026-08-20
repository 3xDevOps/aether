package scheduler

import (
	"errors"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

func TestProvisioningFailureReasonRedactsSetupDiagnostics(t *testing.T) {
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)
	secret := "workspace-secret-9f4d"
	e.ws.Environment.Variables["AETHER_SECRET"] = secret
	e.ws.Environment.SetupPolicy.Script = "set -x; echo $AETHER_SECRET"
	if err := e.db.UpdateWorkspace(t.Context(), e.ws); err != nil {
		t.Fatalf("UpdateWorkspace: %v", err)
	}
	e.rt.startErr = errors.New("runtime: setup script exited 17: + echo " + secret + "\n/srv/aether/run-secret")
	t.Setenv(fakeAgentEnv, "fake-agent")

	_, launchErr := e.sched.Launch(t.Context(), e.sess.ID, e.member.ID, "task", "fake", domain.LaunchTUI)
	if launchErr == nil {
		t.Fatal("Launch succeeded despite setup failure")
	}
	if strings.Contains(launchErr.Error(), secret) {
		t.Fatalf("Launch error leaked configured environment value: %q", launchErr)
	}

	prov := waitStatusEvent(t, sub, "", domain.RunProvisioning)
	failed := waitStatusEvent(t, sub, prov.RunID, domain.RunFailed)
	reason := failed.Payload.(events.RunStatusPayload).Reason
	if !strings.Contains(reason, "setup script") {
		t.Fatalf("failure reason lost setup classification: %q", reason)
	}
	for _, leaked := range []string{secret, "set -x", "/srv/aether/run-secret"} {
		if strings.Contains(reason, leaked) {
			t.Fatalf("failure reason leaked %q: %q", leaked, reason)
		}
	}
	if len([]rune(reason)) > maxPublicRunStatusReason {
		t.Fatalf("failure reason length = %d, want <= %d", len([]rune(reason)), maxPublicRunStatusReason)
	}
}

func TestPublicProvisioningReasonPreservesImageFailureClassification(t *testing.T) {
	got := publicRunStatusReason("provisioning: create container: no such image")
	if !strings.Contains(got, "create container") || !strings.Contains(got, "no such image") {
		t.Fatalf("reason = %q, want non-sensitive image classification", got)
	}
}
