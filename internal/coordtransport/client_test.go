package coordtransport

import (
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestCallTimeoutCoversLongPollWait(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		params any
		want   time.Duration
	}{
		{"plan show waits", protocol.MethodMissionPlanShow, protocol.MissionPlanShowParams{WaitSeconds: 30}, 30*time.Second + callMargin},
		{"plan show pointer params", protocol.MethodMissionPlanShow, &protocol.MissionPlanShowParams{WaitSeconds: 30}, 30*time.Second + callMargin},
		{"plan show clamps above the server bound", protocol.MethodMissionPlanShow, protocol.MissionPlanShowParams{WaitSeconds: 300}, 30*time.Second + callMargin},
		{"plan show without a wait", protocol.MethodMissionPlanShow, protocol.MissionPlanShowParams{}, callMargin},
		{"inbox is unchanged", protocol.MethodCoordInbox, protocol.CoordInboxParams{WaitSeconds: 30}, 30*time.Second + callMargin},
		{"other methods keep the default", protocol.MethodCoordStatus, nil, callDefaultTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := callTimeout(tc.method, tc.params)
			if got != tc.want {
				t.Fatalf("callTimeout(%s) = %s, want %s", tc.method, got, tc.want)
			}
		})
	}
	if got := callTimeout(protocol.MethodMissionPlanShow, protocol.MissionPlanShowParams{WaitSeconds: 30}); got <= 30*time.Second {
		t.Fatalf("mission.plan.show deadline = %s, want more than the 30s wait", got)
	}
}
