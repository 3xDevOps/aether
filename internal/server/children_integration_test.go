//go:build integration

package server

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// TestIntegrationChildRunEndToEnd drives child runs through the whole wired
// server: a parent agent starts a child over its own socket, the child
// reports to its parent's inbox, the parent may then report success, and a
// killed parent takes its attached child with it.
func TestIntegrationChildRunEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	t.Setenv("AETHER_FAKE_AGENT", "fake-agent {task}")
	e, srv := newCoordEnv(ctx, t, false)
	release := make(chan struct{})
	defer close(release)

	const (
		parentTask = "child parent task"
		childTask  = "child review task"
		orphanTask = "child orphan parent"
		lateTask   = "child orphaned review"
		verdict    = "LGTM from the child"
	)
	call := func(c *e2eContainer, method string, params, out any) error {
		dir, _ := c.mount(coordtransport.MountDir)
		return coordtransport.Call(ctx, filepath.Join(dir.HostPath, coordtransport.SocketName), method, params, out)
	}
	e.e2e(t).script(parentTask, func(c *e2eContainer) {
		var refused protocol.CoordReportResult
		var receipt protocol.ChildReceipt
		if err := call(c, protocol.MethodChildStart, protocol.ChildStartParams{
			Harness: "fake", Purpose: "review", Task: childTask, IdempotencyKey: "review-1",
		}, &receipt); err != nil {
			c.output("agent-error: child start: " + err.Error() + "\r\n")
			return
		}
		c.output("child:" + receipt.RunID + "\r\n")
		if err := call(c, protocol.MethodCoordReport, protocol.CoordReportParams{
			Outcome: "success", Summary: "too early", IdempotencyKey: "early",
		}, &refused); err == nil || !strings.Contains(err.Error(), "unfinished") {
			c.output(fmt.Sprintf("agent-error: early success = %v\r\n", err))
			return
		}
		ack := ""
		for deadline := time.Now().Add(coordPoll); time.Now().Before(deadline); {
			var in protocol.CoordInboxResult
			if err := call(c, protocol.MethodCoordInbox, protocol.CoordInboxParams{AckToken: ack, WaitSeconds: 5}, &in); err != nil {
				c.output("agent-error: inbox: " + err.Error() + "\r\n")
				return
			}
			ack = in.AckToken
			for _, m := range in.Messages {
				if m.FromRunID == receipt.RunID {
					c.output("result:" + m.Body + "\r\n")
					var done protocol.CoordReportResult
					if err := call(c, protocol.MethodCoordReport, protocol.CoordReportParams{
						Outcome: "success", Summary: "reviewed by a child", IdempotencyKey: "done",
					}, &done); err != nil {
						c.output("agent-error: success: " + err.Error() + "\r\n")
						return
					}
					c.output("reported\r\n")
					<-release
					return
				}
			}
		}
		c.output("agent-error: no child report\r\n")
	})
	e.e2e(t).script(childTask, func(c *e2eContainer) {
		var status protocol.CoordStatusResult
		if err := call(c, protocol.MethodCoordStatus, nil, &status); err != nil {
			c.output("agent-error: status: " + err.Error() + "\r\n")
			return
		}
		c.output("parent:" + status.ParentRunID + "\r\n")
		var out protocol.CoordReportResult
		if err := call(c, protocol.MethodCoordReport, protocol.CoordReportParams{
			Outcome: "success", Summary: verdict, IdempotencyKey: "verdict",
		}, &out); err != nil {
			c.output("agent-error: report: " + err.Error() + "\r\n")
			return
		}
		<-release
	})
	e.e2e(t).script(orphanTask, func(c *e2eContainer) {
		var receipt protocol.ChildReceipt
		if err := call(c, protocol.MethodChildStart, protocol.ChildStartParams{
			Harness: "fake", Purpose: "long review", Task: lateTask, IdempotencyKey: "late-1",
		}, &receipt); err != nil {
			c.output("agent-error: child start: " + err.Error() + "\r\n")
			return
		}
		c.output("child:" + receipt.RunID + "\r\n")
		<-release
	})
	e.e2e(t).script(lateTask, func(c *e2eContainer) { <-release })

	ctrl, client := srv.control(t, e.ada.key)
	var settings protocol.ChildSettings
	enabled := true
	if err := ctrl.Call(protocol.MethodChildSettings, protocol.ChildSettingsParams{Enabled: &enabled}, &settings); err != nil || !settings.Enabled {
		t.Fatalf("child.settings = %+v, %v", settings, err)
	}

	parent := e.launch(t, ctrl, parentTask, "fake")
	att := openAttach(t, client, parent.ID)
	defer func() {
		if t.Failed() {
			t.Logf("parent output: %q", att.output())
		}
	}()
	childID := waitValue(t, att, "child:")
	child := waitRun(t, ctrl, childID, func(r protocol.Run) bool { return r.Status == string(domain.RunRunning) })
	if child.ParentRunID != parent.ID || child.RootRunID != parent.ID || child.ChildPurpose != "review" || child.MemberID != string(e.ada.id) {
		t.Fatalf("child snapshot = %+v, want Ada's child of %s", child, parent.ID)
	}
	childAtt := openAttach(t, client, childID)
	childAtt.waitOutput(t, "parent:"+parent.ID)
	att.waitOutput(t, "result:"+verdict)
	att.waitOutput(t, "reported")
	assertNoAgentError(t, att)
	assertNoAgentError(t, childAtt)

	var tree protocol.RunChildrenResult
	if err := ctrl.Call(protocol.MethodRunChildren, protocol.RunChildrenParams{RunID: parent.ID}, &tree); err != nil {
		t.Fatalf("run.children: %v", err)
	}
	if len(tree.Children) != 1 || tree.Children[0].Report == nil || tree.Children[0].Report.Summary != verdict ||
		tree.Children[0].State != protocol.ChildFinished || tree.Usage.Runs != 2 {
		t.Fatalf("run.children = %+v", tree)
	}

	orphanParent := e.launch(t, ctrl, orphanTask, "fake")
	orphanAtt := openAttach(t, client, orphanParent.ID)
	lateID := waitValue(t, orphanAtt, "child:")
	waitRun(t, ctrl, lateID, func(r protocol.Run) bool { return r.Status == string(domain.RunRunning) })
	if err := ctrl.Call(protocol.MethodRunKill, protocol.RunIDParams{RunID: orphanParent.ID}, nil); err != nil {
		t.Fatalf("run.kill: %v", err)
	}
	waitRun(t, ctrl, lateID, func(r protocol.Run) bool { return domain.RunStatus(r.Status).Terminal() })
}

func waitRun(t *testing.T, ctrl *protocol.Client, id string, ok func(protocol.Run) bool) protocol.Run {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		var res protocol.RunResult
		err := ctrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: id}, &res)
		if err == nil && ok(res.Run) {
			return res.Run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s never reached the wanted state: %+v, %v", id, res.Run, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitValue is what an agent printed after prefix, up to the line end.
func waitValue(t *testing.T, att *attachConn, prefix string) string {
	t.Helper()
	att.waitOutput(t, prefix)
	rest := att.output()[strings.Index(att.output(), prefix)+len(prefix):]
	for !strings.Contains(rest, "\r\n") {
		time.Sleep(50 * time.Millisecond)
		rest = att.output()[strings.Index(att.output(), prefix)+len(prefix):]
	}
	return rest[:strings.Index(rest, "\r\n")]
}
