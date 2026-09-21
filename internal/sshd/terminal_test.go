package sshd

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/store"
)

type historyRunLookupStore struct {
	store.Store
	calls atomic.Int64
}

func (s *historyRunLookupStore) GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	s.calls.Add(1)
	return s.Store.GetRun(ctx, id)
}

type historyTestPTY struct {
	*fakePTY
	page   ptyhost.HistoryPage
	err    error
	before string
	query  string
	limit  int
	run    domain.RunID
	calls  int
}

func (p *historyTestPTY) History(_ context.Context, run domain.RunID, before, query string, limit int) (ptyhost.HistoryPage, error) {
	p.calls++
	p.run, p.before, p.query, p.limit = run, before, query, limit
	return p.page, p.err
}

type blockingHistoryTestPTY struct {
	*fakePTY
	release <-chan struct{}
}

func (p *blockingHistoryTestPTY) History(context.Context, domain.RunID, string, string, int) (ptyhost.HistoryPage, error) {
	<-p.release
	return ptyhost.HistoryPage{}, nil
}

func historyTestCursor(fill byte) string {
	raw := make([]byte, sha256.Size+1)
	for i := range raw {
		raw[i] = fill
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func TestTerminalHistoryIsUniversalReadAndMapsResult(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	signer, _ := addMember(t, e, "History result viewer", domain.RoleViewer, false)
	lineCursor := historyTestCursor(1)
	nextCursor := historyTestCursor(2)
	history := &historyTestPTY{
		fakePTY: e.pty,
		page: ptyhost.HistoryPage{
			Lines:      []ptyhost.HistoryLine{{Cursor: lineCursor, Time: 1234, Text: "visible"}},
			NextCursor: nextCursor,
			HasMore:    true,
		},
	}
	e.srv.cfg.PTY = history
	c := controlAs(t, e, signer)
	var got protocol.TerminalHistoryResult
	before := historyTestCursor(3)
	err := c.Call(protocol.MethodTerminalHistory, protocol.TerminalHistoryParams{
		RunID: string(e.run.ID), Before: before, Query: "needle", Limit: 17,
	}, &got)
	if err != nil {
		t.Fatal(err)
	}
	if history.run != e.run.ID || history.before != before || history.query != "needle" || history.limit != 17 {
		t.Fatalf("history call = run %q before %q query %q limit %d", history.run, history.before, history.query, history.limit)
	}
	if len(got.Lines) != 1 || got.Lines[0].Text != "visible" || got.Lines[0].Cursor != lineCursor ||
		got.NextCursor != nextCursor || !got.HasMore {
		t.Fatalf("terminal.history = %+v", got)
	}
}

func TestTerminalHistoryRevalidatesOpenControlChannelMembershipBeforePTY(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	signer, member := addMember(t, e, "History viewer", domain.RoleViewer, false)
	history := &historyTestPTY{fakePTY: e.pty}
	e.srv.cfg.PTY = history

	c := controlAs(t, e, signer)
	if err := c.Call(protocol.MethodTerminalHistory, protocol.TerminalHistoryParams{
		RunID: string(e.run.ID),
	}, &protocol.TerminalHistoryResult{}); err != nil {
		t.Fatalf("approved member terminal.history: %v", err)
	}
	if history.calls != 1 {
		t.Fatalf("approved member PTY history calls = %d, want 1", history.calls)
	}

	if err := controlClient(t, e).Call(protocol.MethodMemberRemove,
		protocol.MemberRemoveParams{MemberID: string(member.ID)}, nil); err != nil {
		t.Fatalf("member.remove: %v", err)
	}

	var rpcErr *protocol.Error
	err := c.Call(protocol.MethodTerminalHistory, protocol.TerminalHistoryParams{
		RunID: string(e.run.ID),
	}, &protocol.TerminalHistoryResult{})
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeDenied {
		t.Fatalf("revoked member terminal.history error = %v, want CodeDenied", err)
	}
	if history.calls != 1 {
		t.Fatalf("revoked member reached PTY history: calls = %d, want 1", history.calls)
	}
}

func TestTerminalHistoryDeniesPendingMemberBeforePTY(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	signer, _ := addMember(t, e, "Pending history viewer", domain.RoleViewer, true)
	history := &historyTestPTY{fakePTY: e.pty}
	e.srv.cfg.PTY = history

	err := controlAs(t, e, signer).Call(protocol.MethodTerminalHistory, protocol.TerminalHistoryParams{
		RunID: string(e.run.ID),
	}, &protocol.TerminalHistoryResult{})
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeDenied {
		t.Fatalf("pending member terminal.history error = %v, want CodeDenied", err)
	}
	if history.calls != 0 {
		t.Fatalf("pending member reached PTY history: calls = %d", history.calls)
	}
}

func TestTerminalHistoryRejectsInvalidCursor(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.srv.cfg.PTY = &historyTestPTY{fakePTY: e.pty, err: ptyhost.ErrInvalidHistoryCursor}
	c := controlClient(t, e)
	err := c.Call(protocol.MethodTerminalHistory, protocol.TerminalHistoryParams{
		RunID: string(e.run.ID), Before: "bad",
	}, &protocol.TerminalHistoryResult{})
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeInvalidParams {
		t.Fatalf("terminal.history error = %v", err)
	}
}

func TestTerminalHistoryRejectsOversizedEncodedCursorBeforePTY(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	history := &historyTestPTY{fakePTY: e.pty}
	e.srv.cfg.PTY = history
	c := controlClient(t, e)
	err := c.Call(protocol.MethodTerminalHistory, protocol.TerminalHistoryParams{
		RunID: string(e.run.ID), Before: strings.Repeat("A", protocol.MaxTerminalHistoryCursorBytes+1),
	}, &protocol.TerminalHistoryResult{})
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeInvalidParams {
		t.Fatalf("terminal.history oversized cursor error = %v", err)
	}
	if history.calls != 0 {
		t.Fatalf("PTY history called %d times", history.calls)
	}
}

func TestTerminalHistoryNormalizesWireBoundsBeforePTY(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	history := &historyTestPTY{fakePTY: e.pty}
	e.srv.cfg.PTY = history
	c := controlClient(t, e)

	if err := c.Call(protocol.MethodTerminalHistory, protocol.TerminalHistoryParams{
		RunID:  string(e.run.ID),
		Before: strings.Repeat("A", protocol.MaxTerminalHistoryCursorBytes),
		Query:  strings.Repeat("é", protocol.MaxTerminalHistoryQueryBytes/2),
	}, &protocol.TerminalHistoryResult{}); err != nil {
		t.Fatalf("terminal.history at wire bounds: %v", err)
	}
	if history.limit != protocol.DefaultTerminalHistoryLimit {
		t.Fatalf("default limit = %d, want %d", history.limit, protocol.DefaultTerminalHistoryLimit)
	}
	if len(history.before) != protocol.MaxTerminalHistoryCursorBytes ||
		len(history.query) != protocol.MaxTerminalHistoryQueryBytes {
		t.Fatalf("PTY bounds = cursor %d query %d", len(history.before), len(history.query))
	}

	if err := c.Call(protocol.MethodTerminalHistory, protocol.TerminalHistoryParams{
		RunID: string(e.run.ID), Limit: protocol.MaxTerminalHistoryLimit + 1,
	}, &protocol.TerminalHistoryResult{}); err != nil {
		t.Fatalf("terminal.history capped limit: %v", err)
	}
	if history.limit != protocol.MaxTerminalHistoryLimit {
		t.Fatalf("capped limit = %d, want %d", history.limit, protocol.MaxTerminalHistoryLimit)
	}
}

func TestTerminalHistoryRejectsMalformedBoundsBeforePTY(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	lookups := &historyRunLookupStore{Store: e.srv.cfg.Store}
	e.srv.cfg.Store = lookups
	history := &historyTestPTY{fakePTY: e.pty}
	e.srv.cfg.PTY = history
	c := controlClient(t, e)

	requests := []struct {
		params  protocol.TerminalHistoryParams
		message string
	}{
		{params: protocol.TerminalHistoryParams{}, message: "run_id is required"},
		{params: protocol.TerminalHistoryParams{RunID: string(e.run.ID), Limit: -1}, message: "limit must not be negative"},
		{params: protocol.TerminalHistoryParams{RunID: string(e.run.ID), Query: strings.Repeat("é", protocol.MaxTerminalHistoryQueryBytes/2+1)}, message: "terminal history query is too long"},
		{params: protocol.TerminalHistoryParams{RunID: string(e.run.ID), Before: "%%%"}, message: "invalid terminal history cursor"},
		{params: protocol.TerminalHistoryParams{RunID: string(e.run.ID), Before: strings.Repeat("A", protocol.MaxTerminalHistoryCursorBytes+1)}, message: "invalid terminal history cursor"},
		{params: protocol.TerminalHistoryParams{RunID: string(e.run.ID), Before: strings.Repeat("A", protocol.MaxTerminalHistoryParamsBytes)}, message: "terminal history params are too large"},
		{params: protocol.TerminalHistoryParams{RunID: "-bad"}, message: "invalid run_id"},
		{params: protocol.TerminalHistoryParams{RunID: ".bad"}, message: "invalid run_id"},
		{params: protocol.TerminalHistoryParams{RunID: "bad..id"}, message: "invalid run_id"},
		{params: protocol.TerminalHistoryParams{RunID: "bad/id"}, message: "invalid run_id"},
		{params: protocol.TerminalHistoryParams{RunID: strings.Repeat("r", 129)}, message: "invalid run_id"},
	}
	for _, request := range requests {
		err := c.Call(protocol.MethodTerminalHistory, request.params, &protocol.TerminalHistoryResult{})
		var rpcErr *protocol.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeInvalidParams || rpcErr.Message != request.message {
			t.Fatalf("terminal.history request %+v error = %v, want CodeInvalidParams message %q", request.params, err, request.message)
		}
	}

	invalidUTF8 := json.RawMessage(`{"run_id":"` + string(e.run.ID) + `","query":"` + string([]byte{0xff}) + `"}`)
	if _, rpcErr := e.srv.dispatch(t.Context(), e.member.ID, protocol.MethodTerminalHistory, invalidUTF8); rpcErr == nil ||
		rpcErr.Code != protocol.CodeInvalidParams || rpcErr.Message != "terminal history query must be valid UTF-8" {
		t.Fatalf("terminal.history invalid UTF-8 error = %v", rpcErr)
	}
	if calls := lookups.calls.Load(); calls != 0 {
		t.Fatalf("malformed requests reached run lookup: calls = %d", calls)
	}
	if history.calls != 0 {
		t.Fatalf("malformed requests reached PTY history: calls = %d", history.calls)
	}
}

func TestTerminalHistoryValidRequestResolvesAndExecutesOnce(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	lookups := &historyRunLookupStore{Store: e.srv.cfg.Store}
	e.srv.cfg.Store = lookups
	history := &historyTestPTY{fakePTY: e.pty}
	e.srv.cfg.PTY = history

	err := controlClient(t, e).Call(protocol.MethodTerminalHistory, protocol.TerminalHistoryParams{
		RunID: string(e.run.ID), Query: "needle",
	}, &protocol.TerminalHistoryResult{})
	if err != nil {
		t.Fatalf("terminal.history: %v", err)
	}
	if calls := lookups.calls.Load(); calls != 1 {
		t.Fatalf("run lookups = %d, want 1", calls)
	}
	if history.calls != 1 {
		t.Fatalf("PTY history calls = %d, want 1", history.calls)
	}
}

func TestTerminalHistoryMapsBackendErrorsWithoutDetails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		err     error
		code    int
		message string
	}{
		{name: "cursor", err: ptyhost.ErrInvalidHistoryCursor, code: protocol.CodeInvalidParams, message: "invalid terminal history cursor"},
		{name: "query", err: ptyhost.ErrHistoryQueryTooLong, code: protocol.CodeInvalidParams, message: "terminal history query is too long"},
		{name: "run", err: ptyhost.ErrInvalidRunID, code: protocol.CodeInvalidParams, message: "invalid run_id"},
		{name: "missing", err: os.ErrNotExist, code: protocol.CodeNotFound, message: "terminal transcript not found"},
		{name: "canceled", err: context.Canceled, code: protocol.CodeUnavailable, message: "terminal history request canceled"},
		{name: "deadline", err: context.DeadlineExceeded, code: protocol.CodeUnavailable, message: "terminal history is unavailable"},
		{name: "no session", err: ptyhost.ErrNoSession, code: protocol.CodeUnavailable, message: "terminal history is unavailable"},
		{name: "session ended", err: ptyhost.ErrSessionEnded, code: protocol.CodeUnavailable, message: "terminal history is unavailable"},
		{name: "internal", err: errors.New("secret backend detail"), code: protocol.CodeInternal, message: "terminal history failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newTestEnv(t, nil)
			e.srv.cfg.PTY = &historyTestPTY{fakePTY: e.pty, err: tt.err}
			err := controlClient(t, e).Call(protocol.MethodTerminalHistory, protocol.TerminalHistoryParams{
				RunID: string(e.run.ID),
			}, &protocol.TerminalHistoryResult{})
			var rpcErr *protocol.Error
			if !errors.As(err, &rpcErr) || rpcErr.Code != tt.code || rpcErr.Message != tt.message {
				t.Fatalf("terminal.history error = %v, want code %d message %q", err, tt.code, tt.message)
			}
		})
	}
}

func TestTerminalHistoryRejectsMalformedBackendPage(t *testing.T) {
	t.Parallel()
	validCursor := historyTestCursor(1)
	tests := []struct {
		name string
		page ptyhost.HistoryPage
	}{
		{name: "empty line cursor", page: ptyhost.HistoryPage{Lines: []ptyhost.HistoryLine{{}}}},
		{name: "malformed line cursor", page: ptyhost.HistoryPage{Lines: []ptyhost.HistoryLine{{Cursor: "not-base64"}}}},
		{name: "oversized line cursor", page: ptyhost.HistoryPage{Lines: []ptyhost.HistoryLine{{Cursor: strings.Repeat("A", protocol.MaxTerminalHistoryCursorBytes+1)}}}},
		{name: "oversized line", page: ptyhost.HistoryPage{Lines: []ptyhost.HistoryLine{{Cursor: validCursor, Text: strings.Repeat("A", protocol.MaxTerminalHistoryLineBytes+1)}}}},
		{name: "malformed continuation", page: ptyhost.HistoryPage{NextCursor: "not-base64", HasMore: true}},
		{name: "oversized continuation", page: ptyhost.HistoryPage{NextCursor: strings.Repeat("A", protocol.MaxTerminalHistoryCursorBytes+1), HasMore: true}},
		{name: "missing continuation", page: ptyhost.HistoryPage{HasMore: true}},
		{name: "unexpected continuation", page: ptyhost.HistoryPage{NextCursor: validCursor}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			e.srv.cfg.PTY = &historyTestPTY{fakePTY: e.pty, page: tt.page}
			err := controlClient(t, e).Call(protocol.MethodTerminalHistory, protocol.TerminalHistoryParams{
				RunID: string(e.run.ID), Limit: protocol.MaxTerminalHistoryLimit,
			}, &protocol.TerminalHistoryResult{})
			var rpcErr *protocol.Error
			if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeInternal ||
				rpcErr.Message != "terminal history returned an invalid page" {
				t.Fatalf("terminal.history error = %v", err)
			}
		})
	}
}

func TestTerminalHistoryAcceptsDecreasingTimestampsInBackendOrder(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	firstCursor := historyTestCursor(1)
	secondCursor := historyTestCursor(2)
	e.srv.cfg.PTY = &historyTestPTY{fakePTY: e.pty, page: ptyhost.HistoryPage{Lines: []ptyhost.HistoryLine{
		{Cursor: firstCursor, Time: 2, Text: "first"},
		{Cursor: secondCursor, Time: 1, Text: "second"},
	}}}
	var got protocol.TerminalHistoryResult
	if err := controlClient(t, e).Call(protocol.MethodTerminalHistory, protocol.TerminalHistoryParams{
		RunID: string(e.run.ID), Limit: protocol.MaxTerminalHistoryLimit,
	}, &got); err != nil {
		t.Fatalf("terminal.history decreasing timestamps: %v", err)
	}
	if len(got.Lines) != 2 ||
		got.Lines[0].Cursor != firstCursor || got.Lines[0].Time != 2 || got.Lines[0].Text != "first" ||
		got.Lines[1].Cursor != secondCursor || got.Lines[1].Time != 1 || got.Lines[1].Text != "second" {
		t.Fatalf("terminal.history line order = %+v", got.Lines)
	}
}

func TestTerminalHistoryAcceptsProductionMaximumPage(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	cursor := strings.Repeat("A", protocol.MaxTerminalHistoryCursorBytes)
	lines := make([]ptyhost.HistoryLine, protocol.MaxTerminalHistoryLimit)
	for i := range lines {
		lines[i] = ptyhost.HistoryLine{Cursor: cursor, Time: int64(i), Text: strings.Repeat("<", 16<<10)}
	}
	e.srv.cfg.PTY = &historyTestPTY{fakePTY: e.pty, page: ptyhost.HistoryPage{
		Lines: lines, NextCursor: cursor, HasMore: true,
	}}
	var got protocol.TerminalHistoryResult
	if err := controlClient(t, e).Call(protocol.MethodTerminalHistory, protocol.TerminalHistoryParams{
		RunID: string(e.run.ID), Limit: protocol.MaxTerminalHistoryLimit,
	}, &got); err != nil {
		t.Fatalf("terminal.history production maximum: %v", err)
	}
	if len(got.Lines) != protocol.MaxTerminalHistoryLimit || got.NextCursor != cursor || !got.HasMore {
		t.Fatalf("terminal.history production maximum metadata = lines %d next %t more %t", len(got.Lines), got.NextCursor == cursor, got.HasMore)
	}
}

func TestTerminalHistoryRejectsAggregateResultOverBudget(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	cursor := strings.Repeat("A", protocol.MaxTerminalHistoryCursorBytes)
	lineCount := protocol.MaxTerminalHistoryResultBytes/protocol.MaxTerminalHistoryLineBytes + 1
	lines := make([]ptyhost.HistoryLine, lineCount)
	for i := range lines {
		lines[i] = ptyhost.HistoryLine{Cursor: cursor, Time: int64(i), Text: strings.Repeat("<", protocol.MaxTerminalHistoryLineBytes)}
	}
	e.srv.cfg.PTY = &historyTestPTY{fakePTY: e.pty, page: ptyhost.HistoryPage{Lines: lines}}
	err := controlClient(t, e).Call(protocol.MethodTerminalHistory, protocol.TerminalHistoryParams{
		RunID: string(e.run.ID), Limit: protocol.MaxTerminalHistoryLimit,
	}, &protocol.TerminalHistoryResult{})
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeInternal ||
		rpcErr.Message != "terminal history returned an invalid page" {
		t.Fatalf("terminal.history aggregate budget error = %v", err)
	}
}

func TestTerminalHistoryHardTimeoutStopsWedgedHandler(t *testing.T) {
	e := newTestEnv(t, nil)
	release := make(chan struct{})
	defer close(release)
	e.srv.cfg.PTY = &blockingHistoryTestPTY{fakePTY: e.pty, release: release}
	params, err := json.Marshal(protocol.TerminalHistoryParams{RunID: string(e.run.ID)})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, rpcErr := e.srv.terminalHistory(context.Background(), e.member.ID, params)
	if rpcErr == nil || rpcErr.Code != protocol.CodeUnavailable || rpcErr.Message != "terminal history is unavailable" {
		t.Fatalf("terminal.history timeout error = %v", rpcErr)
	}
	if elapsed := time.Since(started); elapsed < terminalHistoryTimeout || elapsed > terminalHistoryTimeout+time.Second {
		t.Fatalf("terminal.history timeout elapsed = %v", elapsed)
	}
}

func TestTerminalHistoryPreservesCallerCancellation(t *testing.T) {
	e := newTestEnv(t, nil)
	release := make(chan struct{})
	defer close(release)
	e.srv.cfg.PTY = &blockingHistoryTestPTY{fakePTY: e.pty, release: release}
	params, err := json.Marshal(protocol.TerminalHistoryParams{RunID: string(e.run.ID)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, rpcErr := e.srv.terminalHistory(ctx, e.member.ID, params)
	if rpcErr == nil || rpcErr.Code != protocol.CodeUnavailable || rpcErr.Message != "terminal history request canceled" {
		t.Fatalf("terminal.history canceled error = %v", rpcErr)
	}
}

func TestTerminalControlStatusAndStopAreMemberScoped(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	c := controlClient(t, e)

	var status protocol.TerminalStatusResult
	if err := c.Call(protocol.MethodTerminalStatus, struct{}{}, &status); err != nil {
		t.Fatalf("terminal.status: %v", err)
	}
	if status.Running || status.Image != "" || len(status.Tabs) != 0 {
		t.Fatalf("status = %+v, want empty stopped status", status)
	}
	if err := c.Call(protocol.MethodTerminalStop, struct{}{}, nil); err != nil {
		t.Fatalf("terminal.stop: %v", err)
	}
	calls := e.runs.Calls()
	if len(calls) < 2 || calls[len(calls)-2] != "terminal-status:"+string(e.member.ID) || calls[len(calls)-1] != "terminal-stop:"+string(e.member.ID) {
		t.Fatalf("RunController calls = %v", calls)
	}
}

func TestTerminalAdmissionSerializesMemberRemoval(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := context.Background()
	_, target := addMember(t, e, "Terminal user", domain.RoleCollaborator, false)
	started, release := e.runs.blockEnsureTerminal()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	type terminalResult struct {
		term *LocalTerminal
		ack  protocol.TerminalResponse
		err  error
	}
	terminalDone := make(chan terminalResult, 1)
	go func() {
		term, ack, err := e.srv.Local(target.ID).Terminal(ctx, protocol.TerminalRequest{
			Tab: "main", Cols: 80, Rows: 24,
		})
		terminalDone <- terminalResult{term: term, ack: ack, err: err}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("terminal admission did not reach EnsureTerminal")
	}

	params, err := json.Marshal(protocol.MemberRemoveParams{MemberID: string(target.ID)})
	if err != nil {
		t.Fatal(err)
	}
	removed := make(chan *protocol.Error, 1)
	go func() {
		_, perr := e.srv.memberRemove(ctx, e.member.ID, params)
		removed <- perr
	}()
	select {
	case perr := <-removed:
		t.Fatalf("member.remove returned before terminal admission completed: %+v", perr)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	released = true
	var result terminalResult
	select {
	case result = <-terminalDone:
	case <-time.After(time.Second):
		t.Fatal("terminal admission did not complete after release")
	}
	if result.err != nil || !result.ack.OK {
		t.Fatalf("terminal admission = ack %+v err %v, want success", result.ack, result.err)
	}
	if perr := <-removed; perr != nil {
		t.Fatalf("member.remove after terminal admission: %+v", perr)
	}
	if result.term != nil {
		_ = result.term.Close()
	}
	if _, err := e.store.GetMember(ctx, target.ID); err == nil {
		t.Fatal("member remained after successful removal")
	}
}

func TestTerminalEnvironmentSaveAndResetAreMemberScoped(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	c := controlClient(t, e)
	var saved protocol.EnvSaveResult
	if err := c.Call(protocol.MethodEnvSave, struct{}{}, &saved); err != nil {
		t.Fatalf("env.save: %v", err)
	}
	wantImage := "aether/member-" + string(e.member.ID) + ":1"
	if saved.Image != wantImage {
		t.Fatalf("saved image = %q, want %q", saved.Image, wantImage)
	}
	if err := c.Call(protocol.MethodEnvReset, struct{}{}, nil); err != nil {
		t.Fatalf("env.reset: %v", err)
	}
	calls := e.runs.Calls()
	if len(calls) < 2 || calls[len(calls)-2] != "env-save:"+string(e.member.ID) || calls[len(calls)-1] != "env-reset:"+string(e.member.ID) {
		t.Fatalf("RunController calls = %v", calls)
	}
}

func TestEnvironmentTerminalNotRunningMapsToInvalidState(t *testing.T) {
	t.Parallel()
	if e := rpcError(scheduler.ErrTerminalNotRunning); e.Code != protocol.CodeInvalidState {
		t.Fatalf("terminal not running code = %d, want %d", e.Code, protocol.CodeInvalidState)
	}
}

func TestTerminalSentinelsMapToWireCodes(t *testing.T) {
	t.Parallel()
	if e := rpcError(scheduler.ErrTerminalTabLimit); e.Code != protocol.CodeInvalidState {
		t.Fatalf("tab limit code = %d, want %d", e.Code, protocol.CodeInvalidState)
	}
	if e := rpcError(scheduler.ErrInvalidTerminalTab); e.Code != protocol.CodeInvalidParams {
		t.Fatalf("invalid tab code = %d, want %d", e.Code, protocol.CodeInvalidParams)
	}
}
