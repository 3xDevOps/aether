package scheduler

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/version"
)

const (
	agentModelsTTL     = time.Hour
	agentModelsFailTTL = time.Minute
	agentModelsTimeout = 30 * time.Second
	codexModelsCache   = ".codex/models_cache.json"

	claudeModelsRequest = `{"type":"control_request","request_id":"1","request":{"subtype":"initialize"}}`
	claudeModelsScript  = `exe=$1; req=$2; shift 2
printf '%s\n' "$req" | timeout -s KILL 25 env "$@" "$exe" -p --safe-mode --input-format stream-json --output-format stream-json --verbose --no-session-persistence`

	// The script's arguments are the executable, its JSON requests, then the
	// KEY=VALUE assignments for env.

	// codex exits on stdin EOF before it answers, so stdin stays open until
	// the model/list answer shows up in its output or codex exits.
	codexModelsScript = `exe=$1; init=$2; list=$3; shift 3
out=$(mktemp) || exit 1
{ printf '%s\n' "$init" "$list"
  i=0; while [ $i -lt 250 ] && [ ! -e "$out.code" ] && ! grep -q '"id":2[,}]' "$out"; do sleep 0.1; i=$((i+1)); done; } | { timeout -s KILL 25 env "$@" "$exe" app-server; echo $? >"$out.code"; } >"$out"
code=$(cat "$out.code")
cat "$out"; rm -f "$out" "$out.code"; exit "${code:-1}"`
	codexModelsList = `{"id":2,"method":"model/list","params":{}}`
)

type agentModelsKey struct {
	account domain.MemberID
	name    string
}

type agentModelsEntry struct {
	result protocol.AgentModelsResult
	probed time.Time
}

func (e agentModelsEntry) ttl() time.Duration {
	if e.result.Source == protocol.ModelSourceFallback || e.result.Source == protocol.ModelSourceNone {
		return agentModelsFailTTL
	}
	return agentModelsTTL
}

type codexCacheModel struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Visibility  string `json:"visibility"`
	Priority    int    `json:"priority"`
}

type modelList struct {
	models       []protocol.AgentModel
	defaultLabel string
}

var errNoModelsAnswer = errors.New("no answer")

// AgentModels lists the models agent name offers on account, probing the
// agent in account's environment terminal and caching the answer for an hour.
// A failed probe still answers: with the last good list, else a fallback,
// which is cached for a minute. Another member's account is probed only while
// its owner's terminal is running.
func (s *Scheduler) AgentModels(ctx context.Context, member, account domain.MemberID, name string, refresh bool) (protocol.AgentModelsResult, error) {
	none := protocol.AgentModelsResult{Source: protocol.ModelSourceNone, Models: []protocol.AgentModel{}}
	if name != "claude" && name != "codex" {
		return none, nil
	}
	profile, argvs, err := s.launchProfile(ctx, member, account, name)
	if err != nil {
		return protocol.AgentModelsResult{}, err
	}
	cli := argvs[domain.LaunchTUI]
	if len(cli) == 0 {
		cli = argvs[domain.LaunchHeadless]
	}
	if len(cli) == 0 {
		return none, nil
	}
	if account != member && s.cfg.Homes != nil {
		if _, err = s.accountLogins(ctx, account, profile); err != nil {
			return protocol.AgentModelsResult{}, err
		}
	}
	key := agentModelsKey{account, name}
	if !refresh {
		s.agentModelsMu.Lock()
		entry, ok := s.agentModels[key]
		s.agentModelsMu.Unlock()
		if ok && s.cfg.Now().Sub(entry.probed) < entry.ttl() {
			return entry.result, nil
		}
	}
	start := account == member
	result, _, _ := s.modelProbes.Do(fmt.Sprintf("%s\x00%s\x00%t", account, name, start), func() (any, error) {
		return s.probeAgentModels(context.WithoutCancel(ctx), key, cli[0], profile.EnvPassthrough, start), nil
	})
	return result.(protocol.AgentModelsResult), nil
}

func (s *Scheduler) probeAgentModels(ctx context.Context, key agentModelsKey, executable string, passthrough []string, start bool) protocol.AgentModelsResult {
	ctx, cancel := context.WithTimeout(ctx, agentModelsTimeout)
	defer cancel()
	now := s.cfg.Now().UTC()
	list, err := s.execAgentModels(ctx, key, executable, passthrough, start)
	var result protocol.AgentModelsResult
	if err == nil {
		result = protocol.AgentModelsResult{
			Models:       list.models,
			DefaultLabel: list.defaultLabel,
			Source:       protocol.ModelSourceAgent,
			FetchedAt:    now.Format(time.RFC3339),
		}
	} else {
		result = s.agentModelsFallback(key, err)
	}
	s.agentModelsMu.Lock()
	if s.agentModels == nil {
		s.agentModels = make(map[agentModelsKey]agentModelsEntry)
	}
	s.agentModels[key] = agentModelsEntry{result: result, probed: now}
	s.agentModelsMu.Unlock()
	return result
}

func (s *Scheduler) agentModelsFallback(key agentModelsKey, probeErr error) protocol.AgentModelsResult {
	s.agentModelsMu.Lock()
	prev, ok := s.agentModels[key]
	s.agentModelsMu.Unlock()
	if ok && prev.result.Source == protocol.ModelSourceAgent {
		prev.result.Error = probeErr.Error()
		return prev.result
	}
	result := protocol.AgentModelsResult{Models: []protocol.AgentModel{}, Source: protocol.ModelSourceNone, Error: probeErr.Error()}
	switch key.name {
	case "claude":
		result.Source = protocol.ModelSourceFallback
		result.Models = []protocol.AgentModel{{ID: "opus", Name: "Opus"}, {ID: "sonnet", Name: "Sonnet"}, {ID: "haiku", Name: "Haiku"}}
	case "codex":
		cached, fetchedAt, err := s.readCodexModelsCache(key.account)
		switch {
		case err != nil:
			result.Error += "; read ~/" + codexModelsCache + ": " + err.Error()
		case len(cached.models) > 0:
			result.Source = protocol.ModelSourceCache
			result.Models, result.DefaultLabel, result.FetchedAt = cached.models, cached.defaultLabel, fetchedAt
		}
	}
	return result
}

func (s *Scheduler) readCodexModelsCache(account domain.MemberID) (modelList, string, error) {
	if s.cfg.Homes == nil {
		return modelList{}, "", nil
	}
	data, err := s.cfg.Homes.HomeDownload(account, codexModelsCache)
	if errors.Is(err, memberhome.ErrConfigNotFound) {
		return modelList{}, "", nil
	}
	if err != nil {
		return modelList{}, "", err
	}
	var file struct {
		FetchedAt string            `json:"fetched_at"`
		Models    []codexCacheModel `json:"models"`
	}
	if err = json.Unmarshal(data, &file); err != nil {
		return modelList{}, "", err
	}
	slices.SortStableFunc(file.Models, func(a, b codexCacheModel) int { return a.Priority - b.Priority })
	var list modelList
	for _, m := range file.Models {
		if m.Visibility != "list" {
			continue
		}
		list.models = append(list.models, protocol.AgentModel{ID: m.Slug, Name: m.DisplayName, Description: m.Description})
	}
	return list, file.FetchedAt, nil
}

func (s *Scheduler) execAgentModels(ctx context.Context, key agentModelsKey, executable string, passthrough []string, start bool) (modelList, error) {
	script, args, parse := claudeModelsScript, []string{claudeModelsRequest}, parseClaudeModels
	if key.name == "codex" {
		script, parse = codexModelsScript, parseCodexModels
		args = []string{fmt.Sprintf(`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"aether","version":%q}}}`, version.Version), codexModelsList}
	}
	if start {
		if _, err := s.EnsureTerminal(ctx, key.account); err != nil {
			return modelList{}, fmt.Errorf("%s models: %w", key.name, err)
		}
	}
	sup := s.lookupLiveTerminal(key.account)
	if sup == nil {
		if !start {
			return modelList{}, fmt.Errorf("%s models: the account owner's environment is not running; start it to list this login's models", key.name)
		}
		return modelList{}, fmt.Errorf("%s models: %w", key.name, ErrTerminalNotRunning)
	}

	argv := append([]string{"/bin/sh", "-c", script, "sh", executable}, args...)
	for _, name := range passthrough {
		if value, ok := os.LookupEnv(name); ok && value != "" {
			argv = append(argv, name+"="+value)
		}
	}
	code, stdout, stderr, err := s.cfg.Runtime.Exec(ctx, sup.containerID, argv, sup.home)
	if err != nil {
		return modelList{}, fmt.Errorf("%s models: run in the environment terminal: %w", key.name, err)
	}
	list, err := parse(stdout)
	if errors.Is(err, errNoModelsAnswer) {
		return modelList{}, fmt.Errorf("%s exited %d: %s", key.name, code, strings.TrimSpace(stderr))
	}
	return list, err
}

func scanJSONLines(out string, visit func(line []byte) bool) {
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() && !visit(sc.Bytes()) {
	}
}

type claudeModelsResponse struct {
	Type     string `json:"type"`
	Response *struct {
		Subtype  string `json:"subtype"`
		Error    string `json:"error"`
		Response struct {
			Models []struct {
				Value       string `json:"value"`
				DisplayName string `json:"displayName"`
				Description string `json:"description"`
			} `json:"models"`
		} `json:"response"`
	} `json:"response"`
}

func parseClaudeModels(out string) (modelList, error) {
	var found *claudeModelsResponse
	scanJSONLines(out, func(line []byte) bool {
		var msg claudeModelsResponse
		if json.Unmarshal(line, &msg) == nil && msg.Type == "control_response" && msg.Response != nil {
			found = &msg
		}
		return found != nil
	})
	if found == nil {
		return modelList{}, errNoModelsAnswer
	}
	if found.Response.Subtype == "error" {
		return modelList{}, errors.New(found.Response.Error)
	}
	var list modelList
	for _, m := range found.Response.Response.Models {
		if m.Value == "default" {
			list.defaultLabel = m.Description
			continue
		}
		list.models = append(list.models, protocol.AgentModel{ID: m.Value, Name: m.DisplayName, Description: m.Description})
	}
	if len(list.models) == 0 {
		return modelList{}, errors.New("claude did not report its models; update Claude Code")
	}
	return list, nil
}

type codexModelsResponse struct {
	Method string `json:"method"`
	ID     int    `json:"id"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
	Result struct {
		Data []struct {
			Model       string `json:"model"`
			DisplayName string `json:"displayName"`
			Description string `json:"description"`
			Hidden      bool   `json:"hidden"`
			IsDefault   bool   `json:"isDefault"`
		} `json:"data"`
	} `json:"result"`
}

func parseCodexModels(out string) (modelList, error) {
	var found *codexModelsResponse
	scanJSONLines(out, func(line []byte) bool {
		var msg codexModelsResponse
		if json.Unmarshal(line, &msg) == nil && msg.Method == "" && msg.ID == 2 {
			found = &msg
		}
		return found != nil
	})
	if found == nil {
		return modelList{}, errNoModelsAnswer
	}
	if found.Error != nil {
		return modelList{}, errors.New(found.Error.Message)
	}
	var list modelList
	for _, m := range found.Result.Data {
		if m.Hidden {
			continue
		}
		if m.IsDefault {
			list.defaultLabel = m.DisplayName
		}
		list.models = append(list.models, protocol.AgentModel{ID: m.Model, Name: m.DisplayName, Description: m.Description})
	}
	if len(list.models) == 0 {
		return modelList{}, errors.New("codex did not report any models")
	}
	return list, nil
}
