package coordcli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestDevelopmentRejectsIdentityAndMalformedParametersBeforeDial(t *testing.T) {
	for _, tc := range []struct{ name, group, command, input string }{
		{"run identity", "terminal", "list", `{"run_id":"other"}`},
		{"null identity", "browser", "status", `{"run_id":null}`},
		{"case folded identity", "artifact", "list", `{"RUN_ID":""}`},
		{"unknown field", "terminal", "start", `{"actor":"admin"}`},
		{"nested unknown", "control", "acquire", `{"surface":{"kind":"terminal","run_id":"other"}}`},
		{"wrong type", "terminal", "start", `{"command":"sh"}`},
		{"negative generation", "control", "release", `{"control_generation":-1}`},
		{"not object", "browser", "status", `null`},
		{"trailing JSON", "browser", "status", `{} {}`},
		{"empty file", "browser", "status", ""},
		{"encoding expansion", "terminal", "start", `{"name":"` + strings.Repeat("<", 9000) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := runCLI(t, filepath.Join(t.TempDir(), "missing.sock"), []string{tc.group, tc.command, "--params-file", "-"}, tc.input)
			env := decodeEnvelope(t, raw)
			if code != ExitUsage || env.Error == nil || env.Error.Code != protocol.CodeInvalidParams {
				t.Fatalf("invalid input reached transport or lost usage status: %d %s", code, raw)
			}
		})
	}
}

func TestDevelopmentBoundsFileAndStdin(t *testing.T) {
	for _, size := range []int{protocol.MaxDevParamsBytes, protocol.MaxDevParamsBytes + 1} {
		data := []byte(`{"name":"` + strings.Repeat("a", size-len(`{"name":""}`)) + `"}`)
		file := filepath.Join(t.TempDir(), "params.json")
		if err := os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, source := range []string{"-", file} {
			got, err := readDevelopmentParams(source, bytes.NewReader(data))
			if size > protocol.MaxDevParamsBytes {
				if errorCode(err) != protocol.CodeInvalidParams {
					t.Fatalf("oversized %s: %v", source, err)
				}
			} else if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("exact-limit %s was truncated/refused: %v", source, err)
			}
		}
	}
	// Bound reads even for an input stream that has not ended.
	reader := &countingDevelopmentReader{}
	if _, err := readDevelopmentParams("-", reader); errorCode(err) != protocol.CodeInvalidParams || reader.n != protocol.MaxDevParamsBytes+1 {
		t.Fatalf("unbounded stdin read: bytes=%d err=%v", reader.n, err)
	}
}

type countingDevelopmentReader struct{ n int }

func (r *countingDevelopmentReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	r.n += len(p)
	return len(p), nil
}

func TestDevelopmentRejectsGenericRPCAndIdentityFlags(t *testing.T) {
	for _, args := range [][]string{
		{"terminal", "rpc", "--method", "coord.report"},
		{"browser", "open", "--run-id", "other"},
		{"artifact", "get", "--socket", "/tmp/other"},
		{"control", "acquire", "extra"},
	} {
		code, _ := runCLI(t, filepath.Join(t.TempDir(), "missing.sock"), args, "")
		if code != ExitUsage {
			t.Fatalf("%v: exit=%d", args, code)
		}
	}
}

func TestDevelopmentRefusalPreservesServerError(t *testing.T) {
	s := newCLISocket(t, func(protocol.Request) protocol.Response {
		return protocol.Response{Error: &protocol.Error{Code: protocol.CodeConflict, Message: "dev.terminal.input: controller generation changed"}}
	})
	code, raw := runCLI(t, s.path, []string{"terminal", "input", "--params-file", "-"}, `{"terminal_id":"t","incarnation":"i","control_session_id":"c","control_generation":1,"kind":"text","text":"hello"}`)
	env := decodeEnvelope(t, raw)
	if code != ExitDenied || env.OK || env.Error == nil || env.Error.Code != protocol.CodeConflict || !strings.Contains(env.Error.Message, "controller generation changed") {
		t.Fatalf("lost controller refusal: %d %s", code, raw)
	}
}

func TestDevelopmentSkillUsesLiveCapabilitySubset(t *testing.T) {
	s := newCLISocket(t, func(protocol.Request) protocol.Response {
		data, _ := json.Marshal(protocol.CoordStatusResult{RunID: "run", Capabilities: []string{protocol.MethodDevTerminalList}})
		return protocol.Response{Result: data}
	})
	code, raw := runCLI(t, s.path, []string{"skill", "terminal"}, "")
	if code != ExitOK || !strings.Contains(raw, "aether-internal terminal list") {
		t.Fatalf("lost available terminal observation: %d %s", code, raw)
	}
	for _, unavailable := range []string{"aether-internal terminal start", "aether-internal control acquire", "aether-internal browser open", "aether-internal artifact get"} {
		if strings.Contains(raw, unavailable) {
			t.Fatalf("advertised absent capability %q", unavailable)
		}
	}
}

func TestDevelopmentSkillGitMarksPullRequestsWithRunID(t *testing.T) {
	s := newCLISocket(t, func(protocol.Request) protocol.Response {
		data, _ := json.Marshal(protocol.CoordStatusResult{RunID: "run-7", Capabilities: []string{protocol.MethodDevTerminalStart}})
		return protocol.Response{Result: data}
	})
	code, raw := runCLI(t, s.path, []string{"skill", "git"}, "")
	if code != ExitOK || !strings.Contains(raw, "\n  Opened from Aether run run-7\n") {
		t.Fatalf("git skill lost the pull request run marker: %d %s", code, raw)
	}
}
