package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

func terminalDimensions(cols, rows uint) error {
	if cols == 0 || rows == 0 || cols > protocol.MaxDevTerminalDimension || rows > protocol.MaxDevTerminalDimension {
		return fmt.Errorf("terminal dimensions must be between 1 and %d", protocol.MaxDevTerminalDimension)
	}
	return nil
}

func terminalPosition(position ptyhost.TerminalPosition) protocol.DevOutputCursor {
	return protocol.DevOutputCursor{Epoch: string(position.Epoch), Sequence: uint64(position.Sequence)}
}

func terminalAfter(cursor *protocol.DevOutputCursor) *ptyhost.TerminalPosition {
	if cursor == nil {
		return nil
	}
	return &ptyhost.TerminalPosition{Epoch: ptyhost.TerminalEpoch(cursor.Epoch), Sequence: ptyhost.TerminalSequence(cursor.Sequence)}
}

func terminalColor(color ptyhost.ScreenColor) string {
	switch color.Mode {
	case "indexed":
		return fmt.Sprintf("indexed:%d", color.Value)
	case "rgb":
		return fmt.Sprintf("#%06x", color.Value&0xffffff)
	default:
		return "default"
	}
}

func terminalScreen(terminal protocol.DevTerminal, observation ptyhost.ScreenObservation) protocol.DevTerminalScreenResult {
	terminal.Cols, terminal.Rows = observation.Cols, observation.Rows
	result := protocol.DevTerminalScreenResult{Terminal: terminal, Position: terminalPosition(observation.Position),
		ScreenRevision: observation.Revision, GeometryRevision: observation.GeometryRevision, Alternate: observation.Alternate,
		Cursor: protocol.DevTerminalCursor{X: observation.Cursor.X, Y: observation.Cursor.Y, Visible: observation.Cursor.Visible},
		Text:   observation.Text, Lines: make([]protocol.DevTerminalRow, len(observation.Lines)),
		ProtocolError: observation.ProtocolError, UnsupportedGraphics: observation.UnsupportedGraphics,
		Palette: make([]string, len(observation.Palette))}
	for i, color := range observation.Palette {
		result.Palette[i] = fmt.Sprintf("#%06x", color&0xffffff)
	}
	for y, row := range observation.Lines {
		line := protocol.DevTerminalRow{Text: row.Text, Wrapped: row.Wrapped, Cells: make([]protocol.DevTerminalCell, len(row.Cells))}
		for x, cell := range row.Cells {
			line.Cells[x] = protocol.DevTerminalCell{Text: cell.Text, Width: cell.Width,
				Foreground: terminalColor(cell.Foreground), Background: terminalColor(cell.Background),
				UnderlineColor: terminalColor(cell.UnderlineColor), Bold: cell.Bold, Dim: cell.Dim, Italic: cell.Italic,
				Underline: cell.Underline != 0, UnderlineStyle: cell.Underline, Blink: cell.Blink, Inverse: cell.Inverse,
				Hidden: cell.Invisible, Strikethrough: cell.Strikethrough, Overline: cell.Overline, Protected: cell.Protected}
		}
		result.Lines[y] = line
	}
	return result
}

func (s *Scheduler) developmentTerminalOutput(ctx context.Context, run domain.RunID, params protocol.DevTerminalOutputParams) (protocol.DevTerminalOutputResult, error) {
	var result protocol.DevTerminalOutputResult
	if params.MaxBytes < 0 || params.MaxBytes > protocol.MaxDevOutputBytes {
		return result, fmt.Errorf("terminal output limit out of bounds")
	}
	if params.Format != "" && params.Format != "text" && params.Format != "raw" {
		return result, fmt.Errorf("terminal output format must be text or raw")
	}
	limit := params.MaxBytes
	if limit == 0 {
		limit = protocol.MaxDevOutputBytes
	}
	if params.Format != "raw" {
		limit = min(limit, (protocol.MaxDevResultBytes-2048)/6)
	}
	lock := s.lockForShell(run)
	lock.Lock()
	defer lock.Unlock()
	set, err := s.loadRunTerminalsLocked(ctx, run)
	if err != nil {
		return result, err
	}
	terminal, err := terminalTarget(set, params.DevTerminalTarget)
	if err != nil {
		return result, err
	}
	if err = s.refreshRunTerminalLocked(ctx, run, set, terminal); err != nil {
		return result, err
	}
	if terminal.Generation == 0 {
		return result, fmt.Errorf("%w: %s", runtime.ErrExecUnavailable, terminal.Unavailable)
	}
	host, ok := s.cfg.PTY.(DevelopmentPTYHost)
	if !ok {
		return result, runtime.ErrExecUnavailable
	}
	observation, err := host.ReadSessionOutput(ptyhost.RunShellSession(run, terminal.ID), ptyhost.OutputRequest{
		After: terminalAfter(params.After), Generation: terminal.Generation, MaxBytes: limit})
	if err != nil {
		return result, err
	}
	result = protocol.DevTerminalOutputResult{Terminal: terminalDescription(terminal), Start: terminalPosition(observation.Start),
		Next: terminalPosition(observation.Next), Position: terminalPosition(observation.Position),
		MissingCursor: observation.MissingCursor, Truncated: observation.Truncated, More: observation.More}
	if params.Format == "raw" {
		result.Data = observation.Data
	} else {
		result.Text = string(observation.Data)
	}
	return result, nil
}

func (s *Scheduler) developmentTerminalInput(ctx context.Context, run domain.RunID, principal control.Principal, params protocol.DevTerminalInputParams, authorize func() error) (protocol.DevTerminalInputResult, error) {
	var result protocol.DevTerminalInputResult
	_, observation, err := s.CaptureDevelopmentTerminal(ctx, run, params.DevTerminalTarget)
	if err != nil {
		return result, err
	}
	data, err := encodeTerminalInput(params, observation)
	if err != nil {
		return result, err
	}
	admission, err := s.DevelopmentTerminalAdmission(ctx, run, principal, params.DevTerminalTarget, params.DevControlFence, authorize)
	if err != nil {
		return result, err
	}
	if admission.Generation != observation.Generation {
		return result, ptyhost.ErrSessionReplaced
	}
	err = s.cfg.PTY.(DevelopmentPTYHost).WriteSessionInput(ctx, ptyhost.RunShellSession(run, params.TerminalID), admission, data)
	result.Accepted = err == nil
	return result, err
}

func (s *Scheduler) developmentTerminalResize(ctx context.Context, run domain.RunID, principal control.Principal, params protocol.DevTerminalResizeParams, authorize func() error) (protocol.DevTerminalResizeResult, error) {
	var result protocol.DevTerminalResizeResult
	if err := terminalDimensions(params.Cols, params.Rows); err != nil {
		return result, err
	}
	admission, err := s.DevelopmentTerminalAdmission(ctx, run, principal, params.DevTerminalTarget, params.DevControlFence, authorize)
	if err != nil {
		return result, err
	}
	host, ok := s.cfg.PTY.(DevelopmentPTYHost)
	if !ok {
		return result, runtime.ErrExecUnavailable
	}
	if resizeErr := host.ResizeSession(ctx, ptyhost.RunShellSession(run, params.TerminalID), admission, params.Cols, params.Rows); resizeErr != nil {
		return result, resizeErr
	}
	terminal, observation, err := s.CaptureDevelopmentTerminal(ctx, run, params.DevTerminalTarget)
	if err != nil {
		return result, err
	}
	return protocol.DevTerminalResizeResult{Terminal: terminal, ScreenRevision: observation.Revision, GeometryRevision: observation.GeometryRevision}, nil
}

func (s *Scheduler) developmentTerminalWait(ctx context.Context, run domain.RunID, params protocol.DevTerminalWaitParams, authorize func() error) (result protocol.DevTerminalWaitResult, resultErr error) {
	defer func() {
		if resultErr == nil {
			if err := authorize(); err != nil {
				result, resultErr = protocol.DevTerminalWaitResult{}, err
			}
		}
	}()
	if params.TimeoutMS < 0 || params.TimeoutMS > protocol.MaxDevWaitMS || len(params.Contains) > protocol.MaxDevInputBytes {
		return result, fmt.Errorf("terminal wait limit out of bounds")
	}
	if params.AfterOutput == nil && params.AfterScreenRevision == 0 && params.Contains == "" && !params.Exit {
		return result, fmt.Errorf("terminal wait condition required")
	}
	duration := time.Duration(params.TimeoutMS) * time.Millisecond
	if duration == 0 {
		duration = time.Duration(protocol.MaxDevWaitMS) * time.Millisecond
	}
	deadline := time.Now().Add(duration)
	waitCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if waitCtx.Err() != nil {
			if result.Terminal.TerminalID == "" {
				return result, fmt.Errorf("%w: terminal observation was not admitted", context.DeadlineExceeded)
			}
			result.Matched, result.TimedOut = false, true
			return result, nil
		}
		if err := authorize(); err != nil {
			return result, err
		}
		lock := s.lockForShell(run)
		for !lock.TryLock() {
			select {
			case <-waitCtx.Done():
				if err := ctx.Err(); err != nil {
					return result, err
				}
				if result.Terminal.TerminalID == "" {
					return result, fmt.Errorf("%w: terminal observation was not admitted", context.DeadlineExceeded)
				}
				result.Matched, result.TimedOut = false, true
				return result, nil
			case <-poll.C:
			}
		}
		set, err := s.loadRunTerminalsLocked(waitCtx, run)
		var terminal *runTerminal
		if err == nil {
			terminal, err = terminalTarget(set, params.DevTerminalTarget)
		}
		if err == nil {
			err = s.refreshRunTerminalLocked(waitCtx, run, set, terminal)
		}
		var generation uint64
		if err == nil {
			result.Terminal = terminalDescription(terminal)
			generation = terminal.Generation
		}
		lock.Unlock()
		if err != nil {
			return result, err
		}
		remaining := time.Until(deadline)
		exited := result.Terminal.Process.State == "exited" || result.Terminal.Process.State == "stopped"
		if generation == 0 {
			if !params.Exit || params.AfterOutput != nil || params.AfterScreenRevision != 0 || params.Contains != "" {
				return result, runtime.ErrExecUnavailable
			}
		} else {
			host, ok := s.cfg.PTY.(DevelopmentPTYHost)
			if !ok {
				return result, runtime.ErrExecUnavailable
			}
			if params.AfterOutput != nil || params.AfterScreenRevision != 0 || params.Contains != "" {
				wait := min(max(remaining, time.Nanosecond), 100*time.Millisecond)
				if exited {
					wait = time.Nanosecond
				}
				observation, waitErr := host.WaitSession(waitCtx, ptyhost.RunShellSession(run, params.TerminalID), ptyhost.WaitRequest{
					Generation: generation, AfterRevision: params.AfterScreenRevision, AfterOutput: terminalAfter(params.AfterOutput),
					Contains: params.Contains, Timeout: wait})
				if waitErr != nil {
					if errors.Is(waitErr, context.DeadlineExceeded) && ctx.Err() == nil {
						result.Matched, result.TimedOut = false, true
						return result, nil
					}
					return result, waitErr
				}
				result.Position, result.ScreenRevision, result.GeometryRevision = terminalPosition(observation.Screen.Position), observation.Screen.Revision, observation.Screen.GeometryRevision
				result.ProtocolError, result.MissingCursor, result.Truncated = observation.Screen.ProtocolError, observation.MissingCursor, observation.Truncated
				result.Matched = observation.Matched
			} else {
				observation, observeErr := host.ObserveSession(ptyhost.RunShellSession(run, params.TerminalID))
				if observeErr != nil {
					return result, observeErr
				}
				if observation.Generation != generation {
					return result, ptyhost.ErrSessionReplaced
				}
				result.Position, result.ScreenRevision, result.GeometryRevision = terminalPosition(observation.Position), observation.Revision, observation.GeometryRevision
				result.ProtocolError = observation.ProtocolError
			}
		}
		if params.Exit && exited {
			result.Matched = true
		}
		if result.Matched || remaining <= 0 {
			result.TimedOut = !result.Matched
			return result, nil
		}
		// EOF is a transport observation, not process exit. Poll ownership until
		// the runtime proves all descendants exited, or the bounded wait ends.
		select {
		case <-waitCtx.Done():
		case <-poll.C:
		}
	}
}

func (s *Scheduler) developmentTerminalStop(ctx context.Context, run domain.RunID, principal control.Principal, params protocol.DevTerminalStopParams, authorize func() error) (protocol.DevTerminalStopResult, error) {
	var result protocol.DevTerminalStopResult
	if params.TimeoutMS < 0 || params.TimeoutMS > protocol.MaxDevWaitMS {
		return result, fmt.Errorf("terminal stop timeout out of bounds")
	}
	if s.cfg.Control == nil {
		return result, control.ErrInvalid
	}
	timeout := time.Duration(params.TimeoutMS) * time.Millisecond
	if timeout == 0 {
		timeout = time.Duration(protocol.MaxDevWaitMS) * time.Millisecond
	}
	stopCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	surface := control.Surface{Kind: control.SurfaceTerminal, ID: params.TerminalID, Incarnation: params.Incarnation}
	err := s.cfg.Control.AdmitSurface(string(run), surface, principal, params.ControlSessionID, params.ControlGeneration, func() error {
		lock := s.lockForShell(run)
		lock.Lock()
		defer lock.Unlock()
		set, err := s.loadRunTerminalsLocked(stopCtx, run)
		if err != nil {
			return err
		}
		terminal, err := terminalTarget(set, params.DevTerminalTarget)
		if err != nil {
			return err
		}
		// Stop is permitted for a recovered, unattachable owned execution. A
		// paused live run is still rejected rather than scheduling a future kill.
		if _, liveRunErr := s.ResolveLiveRun(stopCtx, run, false); liveRunErr != nil {
			return liveRunErr
		}
		if authorizeErr := authorize(); authorizeErr != nil {
			return authorizeErr
		}
		err = s.stopRunTerminalLocked(stopCtx, run, set, terminal, min(s.cfg.StopGrace, timeout/2))
		result.Terminal = terminalDescription(terminal)
		result.Stopped = err == nil && terminal.State.Exited
		result.TimedOut = errors.Is(err, context.DeadlineExceeded)
		return err
	})
	if result.TimedOut && ctx.Err() == nil {
		return result, nil
	}
	return result, err
}

func encodeTerminalInput(params protocol.DevTerminalInputParams, screen ptyhost.ScreenObservation) ([]byte, error) {
	if len(params.Text) > protocol.MaxDevInputBytes || !utf8.ValidString(params.Text) {
		return nil, fmt.Errorf("terminal text exceeds limit or is not UTF-8")
	}
	mods, err := terminalModifiers(params.Modifiers)
	if err != nil {
		return nil, err
	}
	var encoded string
	switch params.Kind {
	case "text", "paste":
		if params.Key != "" || params.Mouse != nil || mods != 0 {
			return nil, fmt.Errorf("text/paste cannot include keys, mouse or modifiers")
		}
		encoded = params.Text
		if params.Kind == "paste" {
			encoded = strings.ReplaceAll(strings.ReplaceAll(encoded, "\r\n", "\r"), "\n", "\r")
			if screen.Modes.BracketedPaste {
				encoded = "\x1b[200~" + encoded + "\x1b[201~"
			}
		}
	case "key":
		if params.Text != "" || params.Mouse != nil {
			return nil, fmt.Errorf("key cannot include text or mouse")
		}
		encoded, err = encodeTerminalKey(params.Key, mods, screen.Modes)
	case "mouse":
		if params.Mouse == nil || params.Text != "" || params.Key != "" {
			return nil, fmt.Errorf("mouse input requires only a mouse event")
		}
		encoded, err = encodeTerminalMouse(*params.Mouse, mods, screen)
	default:
		return nil, fmt.Errorf("terminal input kind must be text, paste, key or mouse")
	}
	if err != nil {
		return nil, err
	}
	if len(encoded) > protocol.MaxDevInputBytes {
		return nil, fmt.Errorf("encoded terminal input exceeds limit")
	}
	return []byte(encoded), nil
}

const (
	terminalShift = 1
	terminalAlt   = 2
	terminalCtrl  = 4
	terminalMeta  = 8
)

func terminalModifiers(modifiers []string) (int, error) {
	bits := 0
	for _, modifier := range modifiers {
		var bit int
		switch strings.ToLower(modifier) {
		case "shift":
			bit = terminalShift
		case "alt":
			bit = terminalAlt
		case "ctrl", "control":
			bit = terminalCtrl
		case "meta":
			bit = terminalMeta
		default:
			return 0, fmt.Errorf("unsupported terminal modifier %q", modifier)
		}
		if bits&bit != 0 {
			return 0, fmt.Errorf("duplicate terminal modifier %q", modifier)
		}
		bits |= bit
	}
	return bits, nil
}

func encodeTerminalKey(key string, mods int, modes ptyhost.TerminalModes) (string, error) {
	name := strings.ToLower(key)
	final := ""
	switch name {
	case "up", "arrowup":
		final = "A"
	case "down", "arrowdown":
		final = "B"
	case "right", "arrowright":
		final = "C"
	case "left", "arrowleft":
		final = "D"
	case "home":
		final = "H"
	case "end":
		final = "F"
	}
	if final != "" {
		if mods != 0 {
			return fmt.Sprintf("\x1b[1;%d%s", mods+1, final), nil
		}
		if modes.ApplicationCursorKeys {
			return "\x1bO" + final, nil
		}
		return "\x1b[" + final, nil
	}
	tilde := 0
	switch name {
	case "insert":
		tilde = 2
	case "delete":
		tilde = 3
	case "pageup":
		tilde = 5
	case "pagedown":
		tilde = 6
	}
	if strings.HasPrefix(name, "f") {
		n, err := strconv.Atoi(name[1:])
		if err == nil && n >= 1 && n <= 24 {
			if n <= 4 {
				final = string(rune('P' + n - 1))
				if mods != 0 {
					return fmt.Sprintf("\x1b[1;%d%s", mods+1, final), nil
				}
				return "\x1bO" + final, nil
			}
			tilde = [...]int{15, 17, 18, 19, 20, 21, 23, 24, 25, 26, 28, 29, 31, 32, 33, 34, 42, 43, 44, 45}[n-5]
		}
	}
	if tilde != 0 {
		if mods != 0 {
			return fmt.Sprintf("\x1b[%d;%d~", tilde, mods+1), nil
		}
		return fmt.Sprintf("\x1b[%d~", tilde), nil
	}
	if strings.HasPrefix(name, "kp") || strings.HasPrefix(name, "numpad") {
		part := strings.TrimPrefix(strings.TrimPrefix(name, "numpad"), "kp")
		plain, application := "", ""
		if len(part) == 1 && part[0] >= '0' && part[0] <= '9' {
			plain, application = part, string(rune('p'+part[0]-'0'))
		}
		switch part {
		case "enter":
			plain, application = "\r", "M"
		case "decimal", ".":
			plain, application = ".", "n"
		case "add", "+":
			plain, application = "+", "k"
		case "subtract", "-":
			plain, application = "-", "m"
		case "multiply", "*":
			plain, application = "*", "j"
		case "divide", "/":
			plain, application = "/", "o"
		case "equal", "=":
			plain, application = "=", "X"
		}
		if application == "" {
			return "", fmt.Errorf("unknown terminal keypad key %q", key)
		}
		if modes.ApplicationKeypad {
			if mods != 0 {
				return fmt.Sprintf("\x1bO%d%s", mods+1, application), nil
			}
			return "\x1bO" + application, nil
		}
		key, name = plain, plain
	}
	value := key
	switch name {
	case "enter", "return":
		value = "\r"
	case "escape", "esc":
		value = "\x1b"
	case "backspace":
		value = "\x7f"
	case "space":
		value = " "
	case "tab":
		if mods&terminalShift != 0 {
			if mods == terminalShift {
				return "\x1b[Z", nil
			}
			return fmt.Sprintf("\x1b[1;%dZ", mods+1), nil
		}
		value = "\t"
	}
	if utf8.RuneCountInString(value) != 1 {
		return "", fmt.Errorf("unknown terminal key %q", key)
	}
	if mods&terminalShift != 0 {
		value = strings.ToUpper(value)
	}
	if mods&terminalCtrl != 0 {
		r, _ := utf8.DecodeRuneInString(value)
		switch {
		case r >= 'a' && r <= 'z':
			value = string(r - 'a' + 1)
		case r >= '@' && r <= '_':
			value = string(r - '@')
		case r == ' ' || r == '2':
			value = "\x00"
		case r >= '3' && r <= '7':
			value = string(r - '3' + 27)
		case r == '?' || r == '8' || r == 127:
			value = "\x7f"
		case r == '\r' || r == '\t' || r == '\x1b':
		default:
			return "", fmt.Errorf("control modifier cannot encode key %q", key)
		}
	}
	if mods&(terminalAlt|terminalMeta) != 0 {
		value = "\x1b" + value
	}
	return value, nil
}

func encodeTerminalMouse(mouse protocol.DevTerminalMouse, mods int, screen ptyhost.ScreenObservation) (string, error) {
	mode := screen.Modes.MouseTracking
	if mode == "" || mode == "NONE" {
		return "", fmt.Errorf("terminal application has not enabled mouse reporting")
	}
	if mods&terminalMeta != 0 {
		return "", fmt.Errorf("terminal mouse does not encode meta")
	}
	if screen.Modes.MouseEncoding == "SGR_PIXELS" {
		return "", fmt.Errorf("pixel mouse mode requires pixel geometry unavailable to cell-coordinate input")
	}
	if mouse.X < 0 || mouse.Y < 0 || mouse.X >= int(screen.Cols) || mouse.Y >= int(screen.Rows) {
		return "", fmt.Errorf("terminal mouse coordinates outside viewport")
	}
	button := 3
	switch strings.ToLower(mouse.Button) {
	case "left":
		button = 0
	case "middle":
		button = 1
	case "right":
		button = 2
	case "", "none":
	default:
		return "", fmt.Errorf("unsupported terminal mouse button")
	}
	code := button
	ending := "M"
	switch mouse.Action {
	case "press":
		if button == 3 {
			return "", fmt.Errorf("mouse press requires button")
		}
	case "release":
		if mode == "X10" {
			return "", fmt.Errorf("X10 mode does not report release")
		}
		if button == 3 {
			return "", fmt.Errorf("mouse release requires button")
		}
		if screen.Modes.MouseEncoding == "SGR" {
			ending = "m"
		} else {
			code = 3
		}
	case "move":
		if mode != "ANY" && (mode != "DRAG" || button == 3) {
			return "", fmt.Errorf("terminal mouse mode does not report this motion")
		}
		code |= 32
	case "wheel":
		if mode == "X10" || mouse.Delta == 0 || mouse.Delta < -100 || mouse.Delta > 100 {
			return "", fmt.Errorf("invalid or unsupported mouse wheel event")
		}
		code = 64
		if mouse.Delta > 0 {
			code = 65
		}
	default:
		return "", fmt.Errorf("unsupported terminal mouse action")
	}
	if mouse.Action != "wheel" && mouse.Delta != 0 {
		return "", fmt.Errorf("mouse delta requires wheel action")
	}
	if mode != "X10" {
		if mods&terminalShift != 0 {
			code |= 4
		}
		if mods&terminalAlt != 0 {
			code |= 8
		}
		if mods&terminalCtrl != 0 {
			code |= 16
		}
	}
	var event string
	switch screen.Modes.MouseEncoding {
	case "SGR":
		event = fmt.Sprintf("\x1b[<%d;%d;%d%s", code, mouse.X+1, mouse.Y+1, ending)
	case "", "DEFAULT":
		if mouse.X > 222 || mouse.Y > 222 {
			return "", fmt.Errorf("legacy terminal mouse coordinates exceed encoding range")
		}
		event = string([]byte{27, '[', 'M', byte(code + 32), byte(mouse.X + 33), byte(mouse.Y + 33)})
	default:
		return "", fmt.Errorf("unsupported terminal mouse encoding %q", screen.Modes.MouseEncoding)
	}
	if mouse.Action == "wheel" {
		count := mouse.Delta
		if count < 0 {
			count = -count
		}
		event = strings.Repeat(event, count)
	}
	return event, nil
}
