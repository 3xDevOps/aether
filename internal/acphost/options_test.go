package acphost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

const legacySessionOptions = `"models":{"currentModelId":"provider/a","availableModels":[{"modelId":"provider/a","name":"Same label"},{"modelId":"provider/b","name":"Same label","_meta":{"context":200000}}]},"modes":{"currentModeId":"ask","availableModes":[{"id":"ask","name":"Ask"},{"id":"code","name":"Code"}]}`

// Real SDK connections exercise response/notification ordering without a live
// provider. In particular, a notification before the response must not deadlock
// on a session mutex held across the option request.
func startOptionSession(t *testing.T, result string, handler func(*acp.Connection, string, json.RawMessage) (any, *acp.RequestError)) *Session {
	t.Helper()
	hostIn, agentOut := io.Pipe()
	agentIn, hostOut := io.Pipe()
	var agent *acp.Connection
	agent = acp.NewConnection(func(_ context.Context, method string, params json.RawMessage) (any, *acp.RequestError) {
		switch method {
		case acp.AgentMethodInitialize:
			return map[string]any{"protocolVersion": acp.ProtocolVersionNumber, "agentCapabilities": map[string]any{}}, nil
		case acp.AgentMethodSessionNew:
			return json.RawMessage(result), nil
		default:
			return handler(agent, method, params)
		}
	}, agentOut, agentIn)
	agent.SetLogger(discard)
	t.Cleanup(func() { _ = agentOut.Close(); _ = agentIn.Close(); _ = hostOut.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	s, err := Start(ctx, hostIn, hostOut, Config{LogPath: filepath.Join(t.TempDir(), "options.jsonl"), Cwd: "/workspace", Logger: discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(); _ = agentOut.Close(); <-s.Done() })
	return s
}

func decodedOptions(t *testing.T, s *Session) []map[string]any {
	t.Helper()
	var options []map[string]any
	if err := json.Unmarshal(s.State().ConfigOptions, &options); err != nil {
		t.Fatal(err)
	}
	return options
}

func optionCategory(t *testing.T, s *Session, category string) map[string]any {
	t.Helper()
	for _, option := range decodedOptions(t, s) {
		if option["category"] == category {
			return option
		}
	}
	t.Fatalf("no %s option in %s", category, s.State().ConfigOptions)
	return nil
}

func optionUpdate(t *testing.T, agent *acp.Connection, update any) {
	t.Helper()
	if err := agent.SendNotification(t.Context(), acp.ClientMethodSessionUpdate, map[string]any{"sessionId": "options", "update": update}); err != nil {
		t.Error(err)
	}
}

func TestLegacyOptionsUseAdvertisedIDsAndMethods(t *testing.T) {
	type request struct {
		method string
		params map[string]any
	}
	requests := make(chan request, 4)
	s := startOptionSession(t, `{"sessionId":"options",`+legacySessionOptions+`,"configOptions":[{"id":"effort","name":"Effort","type":"select","currentValue":"low","options":[{"value":"low","name":"Low"}]}]}`, func(_ *acp.Connection, method string, raw json.RawMessage) (any, *acp.RequestError) {
		var params map[string]any
		_ = json.Unmarshal(raw, &params)
		requests <- request{method, params}
		if method == acp.AgentMethodSessionSetConfigOption {
			return map[string]any{"configOptions": []any{}}, nil
		}
		return map[string]any{}, nil
	})
	model := optionCategory(t, s, "model")
	values := model["options"].([]any)
	if len(values) != 2 || values[1].(map[string]any)["_meta"].(map[string]any)["context"] != float64(200000) {
		t.Fatalf("legacy models lost values or metadata: %v", values)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.SetOption(ctx, model["id"].(string), "provider/b"); err != nil {
		t.Fatal(err)
	}
	if got := <-requests; got.method != "session/set_model" || got.params["modelId"] != "provider/b" || got.params["sessionId"] != "options" {
		t.Fatalf("model request %+v", got)
	}
	mode := optionCategory(t, s, "mode")
	if err := s.SetOption(ctx, mode["id"].(string), "code"); err != nil {
		t.Fatal(err)
	}
	if got := <-requests; got.method != acp.AgentMethodSessionSetMode || got.params["modeId"] != "code" {
		t.Fatalf("mode request %+v", got)
	}
	// A complete native replacement must remove effort, not the independent
	// legacy catalogs or the model selected through session/set_model.
	if err := s.SetOption(ctx, "effort", "low"); err != nil {
		t.Fatal(err)
	}
	<-requests
	if got := decodedOptions(t, s); len(got) != 2 {
		t.Fatalf("native replacement retained removed options: %v", got)
	}
	if optionCategory(t, s, "model")["currentValue"] != "provider/b" || optionCategory(t, s, "mode")["currentValue"] != "code" || s.State().Mode != "code" {
		t.Fatalf("selection reset: %+v", s.State())
	}
}

func TestNativeOptionsPreserveGroupsExtensionsAndPrecedence(t *testing.T) {
	values := make([]any, 80)
	for i := range values {
		values[i] = map[string]any{"value": fmt.Sprintf("provider/%d", i), "name": "Model", "_meta": map[string]any{"index": i}}
	}
	native := []map[string]any{
		{"id": "engine", "category": "model", "type": "select", "name": "Engine", "currentValue": "provider/0", "options": []any{map[string]any{"group": "provider", "name": "Provider", "options": values, "_meta": map[string]any{"groupExtension": true}}}, "vendorField": []any{"preserved"}},
		{"id": "permissions", "category": "mode", "type": "select", "name": "Permissions", "currentValue": "ask", "options": []any{map[string]any{"value": "ask", "name": "Ask"}}},
		{"id": "vendor", "category": "_custom", "type": "_future", "currentValue": map[string]any{"opaque": true}, "extra": 42},
	}
	raw, _ := json.Marshal(native)
	requests := make(chan map[string]any, 1)
	s := startOptionSession(t, `{"sessionId":"options",`+legacySessionOptions+`,"configOptions":`+string(raw)+`}`, func(_ *acp.Connection, method string, params json.RawMessage) (any, *acp.RequestError) {
		if method != acp.AgentMethodSessionSetConfigOption {
			t.Errorf("native selector used %s", method)
			return nil, acp.NewMethodNotFound(method)
		}
		var request map[string]any
		_ = json.Unmarshal(params, &request)
		requests <- request
		return map[string]any{"configOptions": json.RawMessage(raw)}, nil
	})
	var expected []map[string]any
	_ = json.Unmarshal(raw, &expected)
	if got := decodedOptions(t, s); !reflect.DeepEqual(got, expected) {
		t.Fatalf("native catalog changed: %s", s.State().ConfigOptions)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.SetOption(ctx, "engine", "provider/79"); err != nil {
		t.Fatal(err)
	}
	if got := <-requests; got["configId"] != "engine" || got["value"] != "provider/79" {
		t.Fatalf("native selection lost opaque IDs: %v", got)
	}
	if got := decodedOptions(t, s); !reflect.DeepEqual(got, expected) {
		t.Fatalf("response changed native options: %s", s.State().ConfigOptions)
	}
}

func TestOptionNotificationsAndNativeReplacementKeepSelections(t *testing.T) {
	step := 0
	s := startOptionSession(t, `{"sessionId":"options",`+legacySessionOptions+`}`, func(agent *acp.Connection, method string, _ json.RawMessage) (any, *acp.RequestError) {
		step++
		switch step {
		case 1:
			if method != "session/set_model" {
				t.Errorf("method %s", method)
			}
			optionUpdate(t, agent, map[string]any{"sessionUpdate": "config_option_update", "configOptions": json.RawMessage(`[{"id":"engine","category":"model","type":"select","currentValue":"provider/a","options":[{"value":"provider/a","name":"A"},{"value":"provider/b","name":"B"}]}]`)})
			return map[string]any{}, nil
		case 2:
			// The native response now selects B; subsequent removal must not
			// resurrect the original legacy currentModelId of A.
			return map[string]any{"configOptions": json.RawMessage(`[{"id":"engine","category":"model","type":"select","currentValue":"provider/b","options":[{"value":"provider/b","name":"B"}]}]`)}, nil
		case 3:
			optionUpdate(t, agent, map[string]any{"sessionUpdate": "config_option_update", "configOptions": []any{}})
			optionUpdate(t, agent, map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": "ask"})
			return map[string]any{}, nil
		}
		return nil, acp.NewMethodNotFound(method)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.SetOption(ctx, optionCategory(t, s, "model")["id"].(string), "provider/b"); err != nil {
		t.Fatal(err)
	}
	if got := optionCategory(t, s, "model"); got["id"] != "engine" || got["currentValue"] != "provider/a" {
		t.Fatalf("synthetic response overwrote native update: %v", got)
	}
	if err := s.SetOption(ctx, "engine", "provider/b"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOption(ctx, optionCategory(t, s, "mode")["id"].(string), "code"); err != nil {
		t.Fatal(err)
	}
	if got := optionCategory(t, s, "model"); got["id"] == "engine" || got["currentValue"] != "provider/b" {
		t.Fatalf("native removal reset model: %v", got)
	}
	if s.State().Mode != "ask" || optionCategory(t, s, "mode")["currentValue"] != "ask" {
		t.Fatalf("requested mode overwrote authoritative notification: %+v", s.State())
	}
}

func TestLegacyOptionCollisionAndRejectionDoNotCorruptState(t *testing.T) {
	rejected := &acp.RequestError{Code: -32602, Message: "model unavailable for this account", Data: map[string]any{"modelId": "provider/b"}}
	requests := make(chan string, 3)
	s := startOptionSession(t, `{"sessionId":"options",`+legacySessionOptions+`,"configOptions":[{"id":"_aether_model","category":"_custom","type":"select","currentValue":"custom","options":[{"value":"custom","name":"Custom"}]}]}`, func(_ *acp.Connection, method string, _ json.RawMessage) (any, *acp.RequestError) {
		requests <- method
		return nil, rejected
	})
	model := optionCategory(t, s, "model")
	id := model["id"].(string)
	if id == "_aether_model" {
		t.Fatal("synthetic ID collided with native option")
	}
	before := string(s.State().ConfigOptions)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.SetOption(ctx, id, "not-advertised"); !errors.Is(err, ErrUnknownOption) {
		t.Fatalf("unknown selection: %v", err)
	}
	if err := s.SetOption(ctx, id, true); err == nil {
		t.Fatal("boolean model accepted")
	}
	select {
	case method := <-requests:
		t.Fatalf("invalid synthetic selection sent %s", method)
	default:
	}
	for _, selectedID := range []string{id, "_aether_model", "unknown-native"} {
		err := s.SetOption(ctx, selectedID, "provider/b")
		var protocolError *acp.RequestError
		if !errors.As(err, &protocolError) || protocolError.Code != rejected.Code || protocolError.Message != rejected.Message {
			t.Fatalf("agent rejection lost: %v", err)
		}
		method := <-requests
		if (selectedID == id && method != "session/set_model") || (selectedID != id && method != acp.AgentMethodSessionSetConfigOption) {
			t.Fatalf("selection %s routed to %s", selectedID, method)
		}
		if string(s.State().ConfigOptions) != before {
			t.Fatal("rejection changed snapshot")
		}
	}
}

func TestModeUpdatesPreserveNonSelectNativeValues(t *testing.T) {
	for _, tc := range []struct {
		name       string
		optionType string
		value      string
	}{
		{"boolean", "boolean", `true`},
		{"opaque_object", "_future", `{"opaque":true}`},
		{"opaque_string", "_future", `"opaque"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			native := fmt.Sprintf(`{"id":"permissions","category":"mode","type":%q,"currentValue":%s,"_meta":{"vendor":true}}`, tc.optionType, tc.value)
			s := startOptionSession(t, `{"sessionId":"options",`+legacySessionOptions+`,"configOptions":[`+native+`]}`, func(agent *acp.Connection, method string, _ json.RawMessage) (any, *acp.RequestError) {
				if method != acp.AgentMethodSessionSetMode {
					return nil, acp.NewMethodNotFound(method)
				}
				optionUpdate(t, agent, map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": "code"})
				return map[string]any{}, nil
			})
			var expected map[string]any
			if err := json.Unmarshal([]byte(native), &expected); err != nil {
				t.Fatal(err)
			}
			if got := optionCategory(t, s, "mode"); !reflect.DeepEqual(got, expected) {
				t.Fatalf("initial legacy mode changed native option: %v", got)
			}
			if s.State().Mode != "ask" {
				t.Fatalf("opaque value replaced legacy mode: %q", s.State().Mode)
			}
			before := string(s.State().ConfigOptions)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := s.SetMode(ctx, "code"); err != nil {
				t.Fatal(err)
			}
			if s.State().Mode != "code" || string(s.State().ConfigOptions) != before {
				t.Fatalf("mode notification changed native values: %+v", s.State())
			}
		})
	}
}
