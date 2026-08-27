package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validEnvironmentDefinition() EnvironmentDefinition {
	created := time.Date(2026, 8, 27, 9, 30, 0, 0, time.UTC)
	return EnvironmentDefinition{
		WorkspaceID: "01j9fyk3v7q8r2m4n6p8s0t1vx",
		Version:     2,
		Dockerfile:  "FROM ubuntu:24.04\nRUN apt-get update && apt-get install -y golang-go=2:1.22~2build1\n",
		Manifest: []ManifestItem{
			{
				Name:         "go",
				Version:      "1.22",
				Reason:       "repository language",
				StartLine:    2,
				EndLine:      2,
				CheckCommand: "go version",
			},
		},
		Source:    EnvironmentSourceMirror,
		Harness:   "claude",
		Status:    EnvironmentSaved,
		CreatedAt: created,
		UpdatedAt: created,
	}
}

func TestEnvironmentDefinitionRoundTripsJSON(t *testing.T) {
	def := validEnvironmentDefinition()
	def.Status = EnvironmentFailed
	def.FailureDetail = "go: declared 1.22, output reported 1.21"

	data, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got EnvironmentDefinition
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(def, got) {
		t.Fatalf("round trip changed the definition:\nbefore %+v\nafter  %+v", def, got)
	}
}

func TestEnvironmentDefinitionValidate(t *testing.T) {
	if err := validEnvironmentDefinition().Validate(); err != nil {
		t.Fatalf("valid definition rejected: %v", err)
	}

	tests := map[string]func(*EnvironmentDefinition){
		"empty dockerfile":     func(d *EnvironmentDefinition) { d.Dockerfile = " \n" },
		"empty manifest":       func(d *EnvironmentDefinition) { d.Manifest = nil },
		"item without check":   func(d *EnvironmentDefinition) { d.Manifest[0].CheckCommand = "  " },
		"item without name":    func(d *EnvironmentDefinition) { d.Manifest[0].Name = "" },
		"item without version": func(d *EnvironmentDefinition) { d.Manifest[0].Version = "" },
		"item with reversed span": func(d *EnvironmentDefinition) {
			d.Manifest[0].StartLine = 3
			d.Manifest[0].EndLine = 2
		},
		"unknown source":       func(d *EnvironmentDefinition) { d.Source = EnvironmentSource("bogus") },
		"unknown status":       func(d *EnvironmentDefinition) { d.Status = EnvironmentStatus("bogus") },
		"missing workspace id": func(d *EnvironmentDefinition) { d.WorkspaceID = "" },
		"negative version":     func(d *EnvironmentDefinition) { d.Version = -1 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			def := validEnvironmentDefinition()
			mutate(&def)
			if err := def.Validate(); err == nil {
				t.Fatal("invalid definition accepted")
			}
		})
	}
}

func TestEnvironmentSourceValid(t *testing.T) {
	for _, s := range []EnvironmentSource{
		EnvironmentSourceMirror, EnvironmentSourceRepo,
		EnvironmentSourceStandard, EnvironmentSourceManual,
	} {
		if !s.Valid() {
			t.Errorf("%q.Valid() = false, want true", s)
		}
	}
	if EnvironmentSource("bogus").Valid() {
		t.Error(`EnvironmentSource("bogus").Valid() = true, want false`)
	}
}

func TestEnvironmentStatusValid(t *testing.T) {
	for _, s := range []EnvironmentStatus{
		EnvironmentSaved, EnvironmentBuilding, EnvironmentVerifying,
		EnvironmentActive, EnvironmentFailed,
	} {
		if !s.Valid() {
			t.Errorf("%q.Valid() = false, want true", s)
		}
	}
	if EnvironmentStatus("bogus").Valid() {
		t.Error(`EnvironmentStatus("bogus").Valid() = true, want false`)
	}
}

func TestEnvironmentDefinitionImageTag(t *testing.T) {
	def := validEnvironmentDefinition()
	tag := def.ImageTag()
	want := "aether/ws-01j9fyk3v7q8r2m4n6p8s0t1vx:2"
	if tag != want {
		t.Fatalf("ImageTag() = %q, want %q", tag, want)
	}
	if def.ImageTag() != tag {
		t.Fatal("ImageTag() is not stable across calls")
	}
	const safe = "abcdefghijklmnopqrstuvwxyz0123456789/-:._"
	for _, r := range tag {
		if !strings.ContainsRune(safe, r) {
			t.Fatalf("ImageTag() %q contains shell-hostile character %q", tag, r)
		}
	}
}
