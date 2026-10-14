package acphost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// optionCatalog keeps native options separate from the legacy selectors. Native
// lists are complete replacements, but must not erase a legacy model or mode.
// Raw option payloads preserve groups, custom types and agent extensions.
// All access is protected by Session.mu.
type optionCatalog struct {
	native  json.RawMessage
	options []nativeOption
	model   legacyOption
	mode    legacyOption
	// Complete native snapshots fence in-flight responses, even when unchanged.
	nativeRevision uint64
}

type nativeOption struct {
	ID       string          `json:"id"`
	Category string          `json:"category"`
	Type     string          `json:"type"`
	Current  json.RawMessage `json:"currentValue"`
	raw      json.RawMessage
}

func (o nativeOption) category() string {
	if o.Category != "" {
		return o.Category
	}
	// Older adapters used the conventional IDs before categories existed.
	if o.ID == "model" || o.ID == "mode" {
		return o.ID
	}
	return ""
}

func (o nativeOption) selectValue() (string, bool) {
	if o.Type != "select" || !present(o.Current) {
		return "", false
	}
	var value string
	err := json.Unmarshal(o.Current, &value)
	return value, err == nil
}

type legacyOption struct {
	current  string
	values   []json.RawMessage
	revision uint64
}

func legacyOptions(raw json.RawMessage, listKey, idKey, currentKey string) legacyOption {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return legacyOption{}
	}
	var result legacyOption
	_ = json.Unmarshal(fields[currentKey], &result.current)
	var entries []map[string]json.RawMessage
	if json.Unmarshal(fields[listKey], &entries) != nil {
		return result
	}
	for _, entry := range entries {
		var id string
		if json.Unmarshal(entry[idKey], &id) != nil || id == "" {
			continue
		}
		// Keep the original ID and all extension fields as well as the value
		// key required by the dashboard's config_options contract.
		entry["value"] = entry[idKey]
		value, _ := json.Marshal(entry)
		result.values = append(result.values, value)
	}
	return result
}

func (o *optionCatalog) legacy(category string) *legacyOption {
	if category == "model" {
		return &o.model
	}
	return &o.mode
}

func (o *optionCatalog) replace(raw json.RawMessage) {
	var entries []json.RawMessage
	if !present(raw) || json.Unmarshal(raw, &entries) != nil {
		return
	}
	o.nativeRevision++
	o.native = raw
	o.options = make([]nativeOption, len(entries))
	var seenModel, seenMode bool
	for i, entry := range entries {
		option := &o.options[i]
		_ = json.Unmarshal(entry, option)
		option.raw = entry
		category := option.category()
		if (category == "model" && !seenModel) || (category == "mode" && !seenMode) {
			if current, ok := option.selectValue(); ok {
				legacy := o.legacy(category)
				legacy.current = current
				legacy.revision++
				seenModel = seenModel || category == "model"
				seenMode = seenMode || category == "mode"
			}
		}
	}
}

func (o *optionCatalog) syntheticID(category string) string {
	if len(o.legacy(category).values) == 0 {
		return ""
	}
	for _, option := range o.options {
		if option.category() == category {
			return ""
		}
	}
	id := "_aether_" + category
	for {
		collision := false
		for _, option := range o.options {
			if option.ID == id {
				collision = true
				break
			}
		}
		if !collision {
			return id
		}
		id += "_"
	}
}

func (o *optionCatalog) nativeModelID() string {
	for _, option := range o.options {
		if option.category() == "model" {
			return option.ID
		}
	}
	return ""
}

func (o *optionCatalog) render() json.RawMessage {
	modelID, modeID := o.syntheticID("model"), o.syntheticID("mode")
	if modelID == "" && modeID == "" {
		return o.native
	}
	options := make([]json.RawMessage, 0, len(o.options)+2)
	for _, option := range o.options {
		options = append(options, option.raw)
	}
	for _, category := range []string{"model", "mode"} {
		id := modelID
		name := "Model"
		if category == "mode" {
			id, name = modeID, "Mode"
		}
		if id == "" {
			continue
		}
		legacy := o.legacy(category)
		raw, _ := json.Marshal(struct {
			ID       string            `json:"id"`
			Name     string            `json:"name"`
			Category string            `json:"category"`
			Type     string            `json:"type"`
			Current  string            `json:"currentValue"`
			Options  []json.RawMessage `json:"options"`
		}{id, name, category, "select", legacy.current, legacy.values})
		options = append(options, raw)
	}
	raw, _ := json.Marshal(options)
	return raw
}

func (o *optionCatalog) selection(id string, value any) (string, error) {
	for _, category := range []string{"model", "mode"} {
		if synthetic := o.syntheticID(category); synthetic == "" || synthetic != id {
			continue
		}
		selected, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("acphost: %s value must be a string", category)
		}
		for _, raw := range o.legacy(category).values {
			var option struct {
				Value string `json:"value"`
			}
			_ = json.Unmarshal(raw, &option)
			if option.Value == selected {
				return category, nil
			}
		}
		return "", fmt.Errorf("%w: %s %q", ErrUnknownOption, category, selected)
	}
	// Native (including unknown) IDs go to the agent, which owns validation
	// and returns its actual protocol error without speculative state changes.
	return "", nil
}

func (o *optionCatalog) setCurrent(category, value string) {
	legacy := o.legacy(category)
	legacy.current = value
	legacy.revision++
	changed := false
	for i := range o.options {
		option := &o.options[i]
		if option.category() != category {
			continue
		}
		if _, ok := option.selectValue(); !ok {
			continue
		}
		current, _ := json.Marshal(value)
		if bytes.Equal(option.Current, current) {
			break
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(option.raw, &fields) != nil {
			continue
		}
		option.Current = current
		fields["currentValue"] = option.Current
		option.raw, _ = json.Marshal(fields)
		changed = true
		break
	}
	if changed {
		entries := make([]json.RawMessage, len(o.options))
		for i, option := range o.options {
			entries[i] = option.raw
		}
		o.native, _ = json.Marshal(entries)
	}
}

func (s *Session) replaceOptionsLocked(raw json.RawMessage) {
	s.options.replace(raw)
	s.publishOptionsLocked()
}

func (s *Session) publishOptionsLocked() {
	s.emitOptionsLocked()
	if mode := s.options.mode.current; mode != "" && mode != s.state.Mode {
		s.emitLocked(Item{Kind: KindModeChange, Mode: mode})
	}
}

func (s *Session) emitOptionsLocked() {
	if raw := s.options.render(); present(raw) && !bytes.Equal(raw, s.state.ConfigOptions) {
		s.emitLocked(Item{Kind: KindConfigOptions, ConfigOptions: raw})
	}
}

func (s *Session) projectLocked(it Item) {
	if it.Kind == KindConfigOptions {
		s.replaceOptionsLocked(it.ConfigOptions)
		return
	}
	s.emitLocked(it)
}

// SetOption sets an advertised option, using the legacy API only for selectors
// synthesized from the agent's models or modes.
func (s *Session) SetOption(ctx context.Context, configID string, value any) error {
	s.optionMu.Lock()
	defer s.optionMu.Unlock()
	s.mu.Lock()
	category, err := s.options.selection(configID, value)
	nativeRevision := s.options.nativeRevision
	modeRevision := s.options.mode.revision
	var revision uint64
	if category != "" {
		revision = s.options.legacy(category).revision
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if category == "" {
		opts, optionErr := s.conn.setOption(ctx, configID, value)
		if optionErr != nil {
			return optionErr
		}
		if present(opts) {
			s.mu.Lock()
			// Full catalog notifications own the entire snapshot. A mode-only
			// notification owns just mode, not the other accepted response values.
			if s.options.nativeRevision == nativeRevision {
				mode, modeChanged := s.options.mode.current, s.options.mode.revision != modeRevision
				s.options.replace(opts)
				if modeChanged {
					s.options.setCurrent("mode", mode)
				}
				s.publishOptionsLocked()
			}
			s.mu.Unlock()
		}
		return nil
	}
	return s.setLegacy(ctx, category, value.(string), revision) // selection checked the legacy value and type.
}

// setLegacy runs under optionMu; revision is the category's legacy revision
// read before the call.
func (s *Session) setLegacy(ctx context.Context, category, selected string, revision uint64) error {
	var err error
	if category == "model" {
		err = s.conn.setModel(ctx, selected)
	} else {
		err = s.conn.setMode(ctx, selected)
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Notifications can supply the authoritative selection during the RPC.
	// Only fill in the requested value when the agent did not send one.
	if s.options.legacy(category).revision == revision {
		if category == "mode" {
			s.emitLocked(Item{Kind: KindModeChange, Mode: selected})
		} else {
			s.options.setCurrent(category, selected)
			s.emitOptionsLocked()
		}
	}
	return nil
}

// SetModel sets the agent's native model option when it advertises one, and
// calls session/set_model otherwise.
func (s *Session) SetModel(ctx context.Context, model string) error {
	s.mu.Lock()
	id := s.options.nativeModelID()
	s.mu.Unlock()
	if id != "" {
		return s.SetOption(ctx, id, model)
	}
	s.optionMu.Lock()
	defer s.optionMu.Unlock()
	s.mu.Lock()
	revision := s.options.model.revision
	s.mu.Unlock()
	return s.setLegacy(ctx, "model", model, revision)
}

// ModelOptionID is the id SetOption takes to change the model: the native
// model option, else the selector synthesized from the agent's model list,
// else empty.
func (s *Session) ModelOptionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id := s.options.nativeModelID(); id != "" {
		return id
	}
	return s.options.syntheticID("model")
}

// Model is the session's current model, empty when the agent reports none.
func (s *Session) Model() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, option := range s.options.options {
		if option.category() == "model" {
			if current, ok := option.selectValue(); ok {
				return current
			}
		}
	}
	return s.options.model.current
}

// SetMode switches the agent's mode for agents that expose modes but no
// mode config option.
func (s *Session) SetMode(ctx context.Context, modeID string) error {
	s.optionMu.Lock()
	defer s.optionMu.Unlock()
	s.mu.Lock()
	revision := s.options.mode.revision
	s.mu.Unlock()
	if err := s.conn.setMode(ctx, modeID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.options.mode.revision == revision {
		s.emitLocked(Item{Kind: KindModeChange, Mode: modeID})
	}
	return nil
}
