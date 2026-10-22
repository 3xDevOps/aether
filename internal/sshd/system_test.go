package sshd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sysstats"
)

type fakeSystem struct {
	snap  sysstats.Snapshot
	since time.Time
}

func (f *fakeSystem) Read(_ context.Context, since time.Time) (sysstats.Snapshot, error) {
	f.since = since
	return f.snap, nil
}

func TestSystemStatsFiltersContainersToTheCallerAndSumsTheRest(t *testing.T) {
	t.Parallel()
	reader := &fakeSystem{}
	e := newTestEnv(t, func(c *Config) { c.Services.System = reader })
	_, member := addMember(t, e, "Grace", domain.RoleCollaborator, false)
	admin, grace := string(e.member.ID), string(member.ID)
	cores := func(v float64) *sysstats.Rates { return &sysstats.Rates{CPUCores: v} }
	at := time.UnixMilli(1761100005000)
	reader.snap = sysstats.Snapshot{
		Host:         sysstats.HostInfo{CPUCores: 16},
		Samples:      []sysstats.HostSample{{At: at, CPUUser: 12.5}},
		ContainersAt: at,
		Containers: []sysstats.ContainerUsage{
			{Owner: sysstats.Owner{Kind: "run", ID: "run-idle", MemberID: grace}, MemoryBytes: 300},
			{Owner: sysstats.Owner{Kind: "run", ID: "run-busy", MemberID: grace}, MemoryBytes: 100, Rates: cores(2)},
			{Owner: sysstats.Owner{Kind: "run", ID: "run-admin", MemberID: admin}, MemoryBytes: 500, Rates: cores(1.5)},
			{MemoryBytes: 50, Rates: cores(0.25)},
		},
	}
	read := func(id domain.MemberID, since int64) protocol.SystemStatsResult {
		t.Helper()
		raw, err := e.srv.Local(id).Call(t.Context(), protocol.MethodSystemStats, json.RawMessage(fmt.Sprintf(`{"since_ms":%d}`, since)))
		if err != nil {
			t.Fatal(err)
		}
		var result protocol.SystemStatsResult
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"cpu_cores":null`) || strings.Contains(string(raw), `"samples":null`) {
			t.Fatalf("omitted fields must be absent, not null: %s", raw)
		}
		return result
	}

	all := read(e.member.ID, 1761100000000)
	if got := reader.since.UnixMilli(); got != 1761100000000 {
		t.Errorf("since = %d", got)
	}
	if len(all.Containers) != 3 || all.Containers[0].OwnerID != "run-busy" || all.Containers[1].OwnerID != "run-admin" || all.Containers[2].OwnerID != "run-idle" {
		t.Fatalf("admin containers = %+v, want sorted by cpu then memory", all.Containers)
	}
	if all.Containers[2].CPUCores != nil {
		t.Errorf("a container without two samples must omit cpu_cores")
	}
	if all.Others == nil || all.Others.Containers != 1 || all.Others.CPUCores != 0.25 || all.Others.MemoryBytes != 50 {
		t.Errorf("admin others = %+v, want only the unowned container", all.Others)
	}
	if all.ContainersAtMS != 1761100005000 || all.IntervalMS != 5000 || all.RetentionMS != 3600000 || all.Host.CPUCores != 16 || all.Samples[0].TMS != 1761100005000 {
		t.Errorf("envelope = %+v", all)
	}

	own := read(member.ID, 0)
	if len(own.Containers) != 2 || own.Containers[0].OwnerID != "run-busy" || own.Containers[1].OwnerID != "run-idle" {
		t.Fatalf("member containers = %+v, want only their own", own.Containers)
	}
	if own.Others == nil || own.Others.Containers != 2 || own.Others.CPUCores != 1.75 || own.Others.MemoryBytes != 550 {
		t.Errorf("member others = %+v, want the admin run and the unowned container", own.Others)
	}
}

func TestSystemStatsWithoutServiceIsUnavailable(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	pe := wireErrOf(t, controlClient(t, e).Call(protocol.MethodSystemStats, nil, nil))
	if pe.Code != protocol.CodeUnavailable || !strings.HasPrefix(pe.Message, "system.stats: ") {
		t.Errorf("error = %d %q", pe.Code, pe.Message)
	}
}
