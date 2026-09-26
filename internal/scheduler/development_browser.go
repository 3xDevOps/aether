package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/browser"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func browserPage(p *browser.Page) protocol.DevBrowserPage {
	if p == nil {
		return protocol.DevBrowserPage{}
	}
	return protocol.DevBrowserPage{SessionID: p.SessionID, PageID: p.PageID, PageRevision: p.PageRevision, ViewportID: p.ViewportID, URL: p.URL, Title: p.Title, Width: p.Width, Height: p.Height}
}
func browserTarget(p protocol.DevBrowserPageTarget) browser.Request {
	return browser.Request{SessionID: p.SessionID, PageID: p.PageID, PageRevision: p.PageRevision}
}
func browserSurface(session string) control.Surface {
	return control.Surface{Kind: control.SurfaceBrowser, ID: "browser", Incarnation: session}
}
func browserIncarnation(status browser.Status) string {
	if status.SessionID != "" {
		return status.SessionID
	}
	if status.CreationKey != "" {
		return "pending:" + status.CreationKey
	}
	return ""
}
func (s *Scheduler) browserClient(ctx context.Context, live LiveRun, create bool) (browser.Status, *browser.Client, error) {
	d := s.developmentState()
	if d.reason != nil {
		return browser.Status{}, nil, d.reason
	}
	run := browser.Run{ID: string(live.Run.ID), ContainerID: live.ContainerID}
	status, client, err := d.manager.Reconcile(ctx, run)
	if !create || !errors.Is(err, os.ErrNotExist) {
		return status, client, err
	}
	if err := s.reserveBrowser(live.Run.ID); err != nil {
		return status, nil, err
	}
	// An uncertain Create owns its reservation until explicit cleanup; never
	// retry creation merely because a socket/HTTP request failed.
	return d.manager.Ensure(ctx, run)
}
func (s *Scheduler) developmentBrowser(ctx context.Context, id domain.RunID, p control.Principal, method string, raw json.RawMessage, auth func() error) (any, error) {
	timeout := 35 * time.Second
	if method == protocol.MethodDevBrowserOpen {
		timeout = 90 * time.Second
	}
	if method == protocol.MethodDevBrowserReset {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	live, liveErr := s.ResolveLiveRun(ctx, id, method == protocol.MethodDevBrowserStatus)
	if liveErr != nil {
		return nil, liveErr
	}
	d := s.developmentState()
	if method == protocol.MethodDevBrowserStatus {
		var req protocol.DevBrowserStatusParams
		if err := decodeDevelopment(raw, &req); err != nil {
			return nil, err
		}
		out := protocol.DevBrowserStatusResult{DevCapability: protocol.DevCapability{Available: d.reason == nil}, State: "not_started"}
		if d.reason != nil {
			out.Reason = d.reason.Error()
			return out, nil
		}
		status, client, err := s.browserClient(ctx, live, false)
		out.SessionID = browserIncarnation(status)
		if status.State != "" {
			out.State = status.State
		}
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		if err != nil {
			out.Available = false
			out.Reason = err.Error()
			return out, nil
		}
		result, err := client.Do(ctx, browser.Request{Operation: "pages", SessionID: status.SessionID})
		if err != nil {
			return nil, err
		}
		out.Running = true
		out.SelectedPageID = result.SelectedPageID
		return out, nil
	}
	if d.reason != nil {
		return nil, d.reason
	}
	lock := d.lock(id)
	lock.Lock()
	defer lock.Unlock()
	if method == protocol.MethodDevBrowserOpen {
		return s.openDevelopmentBrowser(ctx, live, p, raw, auth)
	}
	status, client, browserErr := s.browserClient(ctx, live, false)
	if browserErr != nil {
		if method == protocol.MethodDevBrowserReset && errors.Is(browserErr, browser.ErrUnavailable) && browserIncarnation(status) != "" {
			return s.restartDevelopmentBrowser(ctx, live, p, raw, browserIncarnation(status), auth)
		}
		return nil, browserErr
	}
	var req browser.Request
	var fence protocol.DevControlFence
	mutation := false
	var convert func(browser.Result) (any, error)
	switch method {
	case protocol.MethodDevBrowserPages:
		var p protocol.DevBrowserPagesParams
		if err := decodeDevelopment(raw, &p); err != nil {
			return nil, err
		}
		req = browser.Request{Operation: "pages", SessionID: p.SessionID}
		convert = func(r browser.Result) (any, error) {
			out := protocol.DevBrowserPagesResult{Pages: []protocol.DevBrowserPage{}, SelectedPageID: r.SelectedPageID}
			for _, page := range r.Pages {
				out.Pages = append(out.Pages, browserPage(&page))
			}
			return out, nil
		}
	case protocol.MethodDevBrowserNavigate:
		var p protocol.DevBrowserNavigateParams
		if err := decodeDevelopment(raw, &p); err != nil {
			return nil, err
		}
		req = browserTarget(p.DevBrowserPageTarget)
		req.Operation = p.Direction
		if req.Operation == "" || req.Operation == "url" {
			req.Operation = "navigate"
		}
		if req.Operation != "navigate" && req.Operation != "back" && req.Operation != "forward" && req.Operation != "reload" {
			return nil, errors.New("invalid navigation direction")
		}
		req.URL = p.URL
		req.TimeoutMS = p.TimeoutMS
		fence = p.DevControlFence
		mutation = true
		convert = func(r browser.Result) (any, error) {
			return protocol.DevBrowserNavigateResult{Page: browserPage(r.Page)}, nil
		}
	case protocol.MethodDevBrowserSnapshot:
		var p protocol.DevBrowserSnapshotParams
		if err := decodeDevelopment(raw, &p); err != nil {
			return nil, err
		}
		if p.MaxNodes == 0 {
			p.MaxNodes = protocol.MaxDevSnapshotNodes
		}
		if p.MaxChars == 0 {
			p.MaxChars = protocol.MaxDevSnapshotChars
		}
		if p.MaxNodes < 1 || p.MaxNodes > protocol.MaxDevSnapshotNodes || p.MaxChars < 1 || p.MaxChars > protocol.MaxDevSnapshotChars {
			return nil, errors.New("snapshot exceeds control response budget")
		}
		req = browserTarget(p.DevBrowserPageTarget)
		req.Operation = "snapshot"
		req.MaxNodes = p.MaxNodes
		req.MaxChars = p.MaxChars
		convert = func(r browser.Result) (any, error) {
			if r.Snapshot == nil {
				return nil, errors.New("companion returned no snapshot")
			}
			out := protocol.DevBrowserSnapshotResult{Page: browserPage(r.Page), Nodes: []protocol.DevBrowserNode{}, Truncated: r.Snapshot.Truncated}
			for _, n := range r.Snapshot.Nodes {
				out.Nodes = append(out.Nodes, protocol.DevBrowserNode{NodeID: n.NodeID, Tag: n.Tag, FrameURL: n.FrameURL, Role: n.Role, Name: n.Name, Text: n.Text, Value: n.Value, Disabled: n.Disabled, Checked: n.Checked, Expanded: n.Expanded})
			}
			return out, nil
		}
	case protocol.MethodDevBrowserAction:
		var p protocol.DevBrowserActionParams
		if err := decodeDevelopment(raw, &p); err != nil {
			return nil, err
		}
		req = browserTarget(p.DevBrowserPageTarget)
		req.Operation = p.Action
		req.Action = p.Phase
		req.NodeID = p.NodeID
		req.ViewportID = p.ViewportID
		req.Text = p.Text
		req.Key = p.Key
		req.Values = p.Values
		req.Modifiers = p.Modifiers
		req.X = p.X
		req.Y = p.Y
		req.DeltaX = p.DeltaX
		req.DeltaY = p.DeltaY
		req.Button = p.Button
		req.TimeoutMS = p.TimeoutMS
		if p.Action == "touch" {
			if req.Action == "down" {
				req.Action = "start"
			}
			if req.Action == "up" {
				req.Action = "end"
			}
		}
		switch p.Action {
		case "click", "fill", "select_option", "key", "scroll", "text", "pointer", "touch", "select":
		default:
			return nil, errors.New("invalid browser action")
		}
		fence = p.DevControlFence
		mutation = true
		convert = func(r browser.Result) (any, error) {
			return protocol.DevBrowserActionResult{Page: browserPage(r.Page)}, nil
		}
	case protocol.MethodDevBrowserScreenshot:
		var p protocol.DevBrowserScreenshotParams
		if err := decodeDevelopment(raw, &p); err != nil {
			return nil, err
		}
		if p.SessionID != status.SessionID {
			return nil, control.ErrStale
		}
		if err := auth(); err != nil {
			return nil, err
		}
		req = browserTarget(p.DevBrowserPageTarget)
		req.FullPage = p.FullPage
		capture, captureErr := client.Capture(ctx, req)
		if captureErr != nil {
			return nil, captureErr
		}
		if err := auth(); err != nil {
			return nil, err
		}
		artifact, err := s.saveDevelopmentCapture(ctx, id, "browser", "", capture, 0)
		return protocol.DevBrowserScreenshotResult{Artifact: artifact}, err
	case protocol.MethodDevBrowserViewport:
		var p protocol.DevBrowserViewportParams
		if err := decodeDevelopment(raw, &p); err != nil {
			return nil, err
		}
		req = browserTarget(p.DevBrowserPageTarget)
		req.Operation = "viewport"
		req.Width = p.Width
		req.Height = p.Height
		fence = p.DevControlFence
		mutation = true
		convert = func(r browser.Result) (any, error) {
			return protocol.DevBrowserViewportResult{Page: browserPage(r.Page)}, nil
		}
	case protocol.MethodDevBrowserWait:
		var p protocol.DevBrowserWaitParams
		if err := decodeDevelopment(raw, &p); err != nil {
			return nil, err
		}
		req = browserTarget(p.DevBrowserPageTarget)
		req.Operation = "wait"
		req.Condition = p.Condition
		req.Text = p.Text
		req.NodeID = p.NodeID
		req.TimeoutMS = p.TimeoutMS
		convert = func(r browser.Result) (any, error) {
			return protocol.DevBrowserWaitResult{Page: browserPage(r.Page), Matched: r.Matched, TimedOut: r.TimedOut}, nil
		}
	case protocol.MethodDevBrowserConsole, protocol.MethodDevBrowserNetwork:
		var p protocol.DevBrowserConsoleParams
		if err := decodeDevelopment(raw, &p); err != nil {
			return nil, err
		}
		if p.Limit == 0 {
			p.Limit = protocol.MaxDevLogEntries
		}
		if p.Limit < 1 || p.Limit > protocol.MaxDevLogEntries {
			return nil, errors.New("log page exceeds limit")
		}
		req = browserTarget(p.DevBrowserPageTarget)
		req.Operation = strings.TrimPrefix(method, "dev.browser.")
		req.After = p.After
		convert = func(r browser.Result) (any, error) {
			missing := len(r.Logs) > 0 && p.After != 0 && r.Logs[0].Sequence > p.After+1
			truncated := r.Truncated
			logs := r.Logs
			if len(logs) > p.Limit {
				logs = logs[:p.Limit]
				truncated = true
			}
			next := p.After
			budget := 0
			console := protocol.DevBrowserConsoleResult{Entries: []protocol.DevBrowserConsoleEntry{}, MissingCursor: missing}
			network := protocol.DevBrowserNetworkResult{Entries: []protocol.DevBrowserNetworkEntry{}, MissingCursor: missing}
			for _, entry := range logs {
				encoded, _ := json.Marshal(entry)
				if budget+len(encoded) > protocol.MaxDevResultBytes/2 {
					truncated = true
					break
				}
				budget += len(encoded)
				next = entry.Sequence
				if req.Operation == "console" {
					console.Entries = append(console.Entries, protocol.DevBrowserConsoleEntry{Sequence: entry.Sequence, Time: entry.CapturedAt.UTC().Format(time.RFC3339Nano), Level: entry.Level, Text: entry.Text, URL: entry.URL})
				} else {
					network.Entries = append(network.Entries, protocol.DevBrowserNetworkEntry{Sequence: entry.Sequence, Time: entry.CapturedAt.UTC().Format(time.RFC3339Nano), URL: entry.URL, Method: entry.Method, Status: entry.Status, Failure: entry.Text})
				}
			}
			console.Next, console.Truncated = next, truncated
			network.Next, network.Truncated = next, truncated
			if req.Operation == "console" {
				return console, nil
			}
			return network, nil
		}
	case protocol.MethodDevBrowserReset:
		var p protocol.DevBrowserResetParams
		if err := decodeDevelopment(raw, &p); err != nil {
			return nil, err
		}
		req = browser.Request{Operation: "reset", SessionID: p.SessionID}
		fence = p.DevControlFence
		mutation = true
		convert = func(r browser.Result) (any, error) {
			return protocol.DevBrowserResetResult{SessionID: r.SessionID}, nil
		}
	case protocol.MethodDevBrowserClose:
		var p protocol.DevBrowserCloseParams
		if err := decodeDevelopment(raw, &p); err != nil {
			return nil, err
		}
		req = browserTarget(p.DevBrowserPageTarget)
		req.Operation = "close"
		fence = p.DevControlFence
		mutation = true
		convert = func(r browser.Result) (any, error) { return protocol.DevBrowserCloseResult{Closed: true}, nil }
	default:
		return nil, errors.New("unknown browser operation")
	}
	if req.SessionID == "" || req.SessionID != status.SessionID {
		return nil, control.ErrStale
	}
	var result browser.Result
	effect := func() error {
		if err := auth(); err != nil {
			return err
		}
		current, err := s.ResolveLiveRun(ctx, id, false)
		if err != nil {
			return err
		}
		if current.ContainerID != live.ContainerID {
			return control.ErrStale
		}
		result, err = client.Do(ctx, req)
		return err
	}
	var operationErr error
	if mutation {
		operationErr = s.cfg.Control.AdmitSurface(string(id), browserSurface(req.SessionID), p, fence.ControlSessionID, fence.ControlGeneration, effect)
	} else {
		operationErr = effect()
	}
	if operationErr != nil {
		return nil, operationErr
	}
	if method == protocol.MethodDevBrowserReset {
		if operationErr = s.markBrowserOpened(id, result.SessionID); operationErr != nil {
			return nil, operationErr
		}
		_, operationErr = s.cfg.Control.RevokeSurface(string(id), browserSurface(req.SessionID), func() error { return nil })
		if operationErr != nil {
			return nil, operationErr
		}
	}
	return convert(result)
}
func (s *Scheduler) openDevelopmentBrowser(ctx context.Context, live LiveRun, p control.Principal, raw json.RawMessage, auth func() error) (any, error) {
	var req protocol.DevBrowserOpenParams
	if err := decodeDevelopment(raw, &req); err != nil {
		return nil, err
	}
	if req.ControlSessionID == "" {
		return nil, errors.New("browser open requires control_session_id")
	}
	if err := (browser.Request{Operation: "open", SessionID: req.SessionID, URL: req.URL, Width: req.Width, Height: req.Height}).Validate(); err != nil {
		return nil, err
	}
	if err := auth(); err != nil {
		return nil, err
	}
	var status browser.Status
	var client *browser.Client
	admissionErr := s.cfg.Control.Admit(string(live.Run.ID), func() error {
		if err := auth(); err != nil {
			return err
		}
		current, err := s.ResolveLiveRun(ctx, live.Run.ID, false)
		if err != nil {
			return err
		}
		if current.ContainerID != live.ContainerID {
			return control.ErrStale
		}
		status, client, err = s.browserClient(ctx, live, true)
		return err
	})
	if admissionErr != nil {
		return nil, admissionErr
	}
	surface := browserSurface(status.SessionID)
	fence := req.DevControlFence
	if req.SessionID == "" {
		if req.ControlGeneration != 0 {
			return nil, control.ErrStale
		}
		previous, readErr := os.ReadFile(s.developmentClosedPath(live.Run.ID) + ".browser-session")
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return nil, readErr
		}
		if string(previous) == status.SessionID {
			return nil, errors.New("browser session already initialized; acquire its current control lease")
		}
		pages, err := client.Do(ctx, browser.Request{Operation: "pages", SessionID: status.SessionID})
		if err != nil {
			return nil, err
		}
		if len(pages.Pages) != 0 {
			return nil, errors.New("browser is already open; acquire its current session before opening another page")
		}
		acquired, _, err := s.cfg.Control.AcquireSurface(string(live.Run.ID), surface, p, req.ControlSessionID, false, 0, auth)
		if err != nil {
			return nil, err
		}
		fence = protocol.DevControlFence{ControlSessionID: acquired.SessionID, ControlGeneration: acquired.Generation}
		if err := s.markBrowserOpened(live.Run.ID, status.SessionID); err != nil {
			_ = s.cfg.Control.ReleaseSurface(string(live.Run.ID), surface, p, fence.ControlSessionID, fence.ControlGeneration, func() error { return nil })
			return nil, err
		}
	} else if req.SessionID != status.SessionID {
		return nil, control.ErrStale
	}
	var result browser.Result
	openErr := s.cfg.Control.AdmitSurface(string(live.Run.ID), surface, p, fence.ControlSessionID, fence.ControlGeneration, func() error {
		if err := auth(); err != nil {
			return err
		}
		current, err := s.ResolveLiveRun(ctx, live.Run.ID, false)
		if err != nil {
			return err
		}
		if current.ContainerID != live.ContainerID {
			return control.ErrStale
		}
		result, err = client.Do(ctx, browser.Request{Operation: "open", SessionID: status.SessionID, URL: req.URL, Width: req.Width, Height: req.Height})
		return err
	})
	if openErr != nil {
		if req.SessionID == "" {
			_ = s.cfg.Control.ReleaseSurface(string(live.Run.ID), surface, p, fence.ControlSessionID, fence.ControlGeneration, func() error { return nil })
		}
		return nil, fmt.Errorf("browser open: %w", openErr)
	}
	return protocol.DevBrowserOpenResult{Page: browserPage(result.Page), Control: fence}, nil
}

func (s *Scheduler) markBrowserOpened(id domain.RunID, session string) error {
	path := s.developmentClosedPath(id) + ".browser-session"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(session), 0o600)
}
func (s *Scheduler) restartDevelopmentBrowser(ctx context.Context, live LiveRun, p control.Principal, raw json.RawMessage, session string, auth func() error) (any, error) {
	var req protocol.DevBrowserResetParams
	if err := decodeDevelopment(raw, &req); err != nil {
		return nil, err
	}
	if req.SessionID != session {
		return nil, control.ErrStale
	}
	d := s.developmentState()
	var fresh browser.Status
	err := s.cfg.Control.AdmitSurface(string(live.Run.ID), browserSurface(session), p, req.ControlSessionID, req.ControlGeneration, func() error {
		if err := auth(); err != nil {
			return err
		}
		if _, err := s.ResolveLiveRun(ctx, live.Run.ID, false); err != nil {
			return err
		}
		if err := s.reserveBrowser(live.Run.ID); err != nil {
			return err
		}
		var err error
		fresh, err = d.manager.Restart(ctx, browser.Run{ID: string(live.Run.ID), ContainerID: live.ContainerID})
		return err
	})
	if err != nil {
		return nil, err
	}
	if err = s.markBrowserOpened(live.Run.ID, fresh.SessionID); err != nil {
		return nil, err
	}
	_, err = s.cfg.Control.RevokeSurface(string(live.Run.ID), browserSurface(session), func() error { return nil })
	return protocol.DevBrowserResetResult{SessionID: fresh.SessionID}, err
}
