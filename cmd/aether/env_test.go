package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestEnvRequiresSubcommand(t *testing.T) {
	err := runEnv(nil)
	if err == nil {
		t.Fatal("env without a subcommand succeeded")
	}
	if got := err.Error(); got != "usage: aether env <show|rebuild|rollback>" {
		t.Fatalf("env usage = %q", got)
	}
}

func TestEnvRejectsUnknownSubcommand(t *testing.T) {
	err := runEnv([]string{"destroy"})
	if err == nil || !strings.Contains(err.Error(), "destroy") {
		t.Fatalf("unknown subcommand error = %v, want it to name the subcommand", err)
	}
}

func TestParseEnvRebuildRejectsNegativeVersion(t *testing.T) {
	if _, err := parseEnvRebuild([]string{"--version", "-2"}); err == nil {
		t.Fatal("negative version accepted")
	}
}

func TestParseEnvRebuildDefaults(t *testing.T) {
	opts, err := parseEnvRebuild(nil)
	if err != nil {
		t.Fatalf("parse rebuild with no flags: %v", err)
	}
	if opts.workspace != "" || opts.version != 0 {
		t.Fatalf("rebuild defaults = %+v, want empty workspace and version 0", opts)
	}
}

func TestPrintEnvStatusEmptyNamesNoVersions(t *testing.T) {
	var out bytes.Buffer
	if err := printEnvStatus(&out, protocol.EnvStatusResult{}); err != nil {
		t.Fatalf("print empty status: %v", err)
	}
	if !strings.Contains(out.String(), "no environment versions") {
		t.Fatalf("empty status output = %q, want it to say no environment versions", out.String())
	}
}

func TestPrintEnvStatusRendersManifestAndVersions(t *testing.T) {
	res := protocol.EnvStatusResult{
		ActiveVersion: 2,
		Versions: []protocol.EnvironmentVersion{
			{
				Version:       3,
				Source:        domain.EnvironmentSourceMirror,
				Status:        domain.EnvironmentFailed,
				FailureDetail: "go: want 1.24, output reported 1.23",
				CreatedAt:     "2026-08-27T10:00:00Z",
			},
			{
				Version: 2,
				Source:  domain.EnvironmentSourceMirror,
				Harness: "claude",
				Status:  domain.EnvironmentActive,
				Active:  true,
				Manifest: []domain.ManifestItem{
					{Name: "go", Version: "1.24.1", Reason: "module builds", CheckCommand: "go version"},
					{Name: "node", Version: "22.14.0", Reason: "dashboard \x1b[2Jtooling", CheckCommand: "node --version"},
				},
				CreatedAt: "2026-08-27T09:00:00Z",
			},
		},
	}
	var out bytes.Buffer
	if err := printEnvStatus(&out, res); err != nil {
		t.Fatalf("print status: %v", err)
	}
	got := out.String()
	for _, want := range []string{"go", "1.24.1", "module builds", "node", "active", "failed", "go: want 1.24, output reported 1.23", "version 2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status output %q is missing %q", got, want)
		}
	}
	if strings.ContainsFunc(got, func(r rune) bool { return r != '\n' && (r < 0x20 || r == 0x7f) }) {
		t.Fatalf("status output carries control characters: %q", got)
	}
}

// envBuildEventLine encodes one environment.build event as the NDJSON the
// event subsystem emits.
func envBuildEventLine(t *testing.T, payload events.EnvironmentBuildPayload) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	line, err := json.Marshal(protocol.Event{Type: string(events.TypeEnvironmentBuild), Payload: raw})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return append(line, '\n')
}

func TestFollowEnvBuildSucceedsOnActive(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(envBuildEventLine(t, events.EnvironmentBuildPayload{Version: 2, Status: domain.EnvironmentBuilding, Line: "Step 1/3 : FROM ubuntu:24.04"}))
	stream.Write(envBuildEventLine(t, events.EnvironmentBuildPayload{Version: 7, Status: domain.EnvironmentFailed, Detail: "another version"}))
	stream.Write(envBuildEventLine(t, events.EnvironmentBuildPayload{Version: 2, Status: domain.EnvironmentVerifying}))
	stream.Write(envBuildEventLine(t, events.EnvironmentBuildPayload{Version: 2, Status: domain.EnvironmentActive}))
	var out bytes.Buffer
	if err := followEnvBuild(&stream, &out, 2); err != nil {
		t.Fatalf("follow build: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "Step 1/3") {
		t.Fatalf("build output %q lost the engine line", got)
	}
	if strings.Contains(got, "another version") {
		t.Fatalf("build output %q leaked another version's events", got)
	}
	if !strings.Contains(got, "active") {
		t.Fatalf("build output %q does not report the active version", got)
	}
}

func TestFollowEnvBuildFailureNamesDetailAndNextCommand(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(envBuildEventLine(t, events.EnvironmentBuildPayload{Version: 3, Status: domain.EnvironmentFailed, Detail: "go: want 1.24, output reported 1.23"}))
	err := followEnvBuild(&stream, &bytes.Buffer{}, 3)
	if err == nil {
		t.Fatal("failed build reported success")
	}
	for _, want := range []string{"go: want 1.24, output reported 1.23", "aether env rollback"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("failure %q is missing %q", err.Error(), want)
		}
	}
}

func TestFollowEnvBuildLostStreamNamesShow(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(envBuildEventLine(t, events.EnvironmentBuildPayload{Version: 3, Status: domain.EnvironmentBuilding, Line: "Step 1/3"}))
	err := followEnvBuild(&stream, &bytes.Buffer{}, 3)
	if err == nil {
		t.Fatal("truncated stream reported success")
	}
	if !strings.Contains(err.Error(), "aether env show") {
		t.Fatalf("lost-stream error %q does not name aether env show", err.Error())
	}
}
