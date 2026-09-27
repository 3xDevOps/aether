package browser

import (
	"math"
	"net/url"
)

func invalid(message string) error             { return &Error{Code: "invalid_request", Message: message} }
func bounded(value, minimum, maximum int) bool { return value >= minimum && value <= maximum }
func finite(value float64) bool                { return !math.IsNaN(value) && !math.IsInf(value, 0) }
func identity(value string) bool               { return value != "" && len(value) <= 256 }

func (r Request) validateTarget() error {
	if !identity(r.SessionID) || !identity(r.PageID) || r.PageRevision == 0 || r.PageRevision > 1<<53-1 {
		return invalid("current session, page, and revision are required")
	}
	return nil
}

// Validate bounds typed operations before transport. The companion repeats the
// checks at its trust boundary and resolves nodes only within the named page.
func (r Request) Validate() error {
	if r.TimeoutMS != 0 && !bounded(r.TimeoutMS, 1, 30000) {
		return invalid("timeout must be in 1..30000 milliseconds")
	}
	if len(r.Modifiers) > 5 {
		return invalid("too many keyboard modifiers")
	}
	for i, modifier := range r.Modifiers {
		switch modifier {
		case "Alt", "Control", "ControlOrMeta", "Meta", "Shift":
		default:
			return invalid("unknown keyboard modifier")
		}
		for _, previous := range r.Modifiers[:i] {
			if modifier == previous {
				return invalid("duplicate keyboard modifier")
			}
		}
	}
	if len(r.Modifiers) != 0 && (r.Operation != "click" && r.Operation != "key") {
		return invalid("modifiers require element click or key press; use separate modifier key down/up events for pointer input")
	}
	if len(r.Modifiers) != 0 && r.Operation == "key" && (r.Action == "down" || r.Action == "up") {
		return invalid("key down/up modifiers must be sent as separate key events")
	}
	switch r.Operation {
	case "pages":
		return nil
	case "reset":
		if !identity(r.SessionID) {
			return invalid("current browser session is required")
		}
		return nil
	case "open":
		if len(r.SessionID) > 256 {
			return invalid("invalid session identity")
		}
		if (r.Width != 0 && !bounded(r.Width, 240, 2560)) || (r.Height != 0 && !bounded(r.Height, 240, 1600)) {
			return invalid("open viewport exceeds dimension limit")
		}
	default:
		if err := r.validateTarget(); err != nil {
			return err
		}
	}
	if len(r.NodeID) > 256 || len(r.ViewportID) > 256 {
		return invalid("target identity exceeds limit")
	}
	switch r.Operation {
	case "open", "navigate":
		if r.Operation == "open" && r.URL == "" {
			return nil
		}
		if r.URL == "about:blank" {
			return nil
		}
		parsed, err := url.Parse(r.URL)
		if err != nil || len(r.URL) > 8192 || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return invalid("only bounded HTTP(S) and about:blank URLs are supported")
		}
	case "select", "close", "back", "forward", "reload":
	case "snapshot":
		if (r.MaxNodes != 0 && !bounded(r.MaxNodes, 1, 128)) || (r.MaxChars != 0 && !bounded(r.MaxChars, 1, 8192)) {
			return invalid("snapshot exceeds node or character limit")
		}
	case "click", "fill", "select_option":
		if !identity(r.NodeID) {
			return invalid("node reference from a current snapshot is required")
		}
		if r.Operation == "click" && r.Button != "" && r.Button != "left" && r.Button != "middle" && r.Button != "right" {
			return invalid("invalid pointer button")
		}
		if r.Operation == "fill" && len(r.Text) > 32000 {
			return invalid("text exceeds limit")
		}
		if r.Operation == "select_option" {
			if len(r.Values) > 100 {
				return invalid("selection exceeds limit")
			}
			for _, value := range r.Values {
				if len(value) > 1024 {
					return invalid("selection value exceeds limit")
				}
			}
		}
	case "text":
		if len(r.Text) > 32000 {
			return invalid("text exceeds limit")
		}
	case "key":
		if !bounded(len(r.Key), 1, 100) || (r.Action != "" && r.Action != "press" && r.Action != "down" && r.Action != "up") {
			return invalid("invalid keyboard input")
		}
	case "pointer", "scroll", "touch":
		if !identity(r.ViewportID) || !finite(r.X) || !finite(r.Y) || r.X < 0 || r.Y < 0 || r.X >= 2560 || r.Y >= 1600 {
			return invalid("current viewport and bounded input coordinates are required")
		}
		switch r.Operation {
		case "pointer":
			if r.Action != "move" && r.Action != "down" && r.Action != "up" && r.Action != "click" {
				return invalid("invalid pointer action")
			}
			if r.Button != "" && r.Button != "left" && r.Button != "middle" && r.Button != "right" {
				return invalid("invalid pointer button")
			}
			if r.ClickCount != 0 && !bounded(r.ClickCount, 1, 3) {
				return invalid("invalid click count")
			}
		case "scroll":
			if !finite(r.DeltaX) || !finite(r.DeltaY) || math.Abs(r.DeltaX) > 10000 || math.Abs(r.DeltaY) > 10000 {
				return invalid("invalid scroll delta")
			}
		case "touch":
			if r.Action != "start" && r.Action != "move" && r.Action != "end" && r.Action != "cancel" {
				return invalid("invalid touch action")
			}
			if r.TouchID != 0 && !bounded(r.TouchID, 1, 10) {
				return invalid("invalid touch identity")
			}
		}
	case "viewport":
		if (r.Width != 0 && !bounded(r.Width, 240, 2560)) || (r.Height != 0 && !bounded(r.Height, 240, 1600)) {
			return invalid("viewport exceeds dimension limit")
		}
	case "wait":
		switch r.Condition {
		case "url", "text":
			if !bounded(len(r.Text), 1, 2048) {
				return invalid("wait text must be non-empty and bounded")
			}
		case "visible", "hidden":
			if !identity(r.NodeID) {
				return invalid("wait requires a current node reference")
			}
		case "load":
		default:
			return invalid("unknown wait condition")
		}
	case "console", "network":
		if r.After > 1<<53-1 {
			return invalid("log cursor exceeds limit")
		}
	default:
		return invalid("unknown browser operation")
	}
	return nil
}

func (s TerminalSnapshot) Validate() error {
	if !identity(s.SessionID) || s.ScreenRevision > 1<<53-1 || s.OutputPosition > 1<<53-1 || s.CapturedAt.IsZero() {
		return invalid("terminal snapshot identity and capture boundary are required")
	}
	if !bounded(s.Cols, 1, 320) || !bounded(s.Rows, 1, 120) || (s.FontSize != 0 && !bounded(s.FontSize, 8, 24)) {
		return invalid("terminal screenshot geometry exceeds limit")
	}
	if len(s.VT) > MaxRequestBytes-4096 {
		return &Error{Code: "resource_limit", Message: "terminal VT exceeds capture limit"}
	}
	return nil
}
