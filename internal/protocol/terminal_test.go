package protocol

import (
	"encoding/json"
	"testing"
)

func TestTerminalRequestAndResultJSON(t *testing.T) {
	req := TerminalRequest{Tab: "logs", Cols: 120, Rows: 40}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var got TerminalRequest
	if uerr := json.Unmarshal(data, &got); uerr != nil {
		t.Fatal(uerr)
	}
	if got != req {
		t.Fatalf("request = %+v, want %+v", got, req)
	}
	result := TerminalStatusResult{Running: true, Image: "standard:latest", StartedAt: "2026-09-03T12:00:00Z", Tabs: []string{"main", "logs"}}
	data, err = json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var gotResult TerminalStatusResult
	if err := json.Unmarshal(data, &gotResult); err != nil {
		t.Fatal(err)
	}
	if gotResult.Running != result.Running || gotResult.Image != result.Image || gotResult.StartedAt != result.StartedAt || len(gotResult.Tabs) != 2 {
		t.Fatalf("result = %+v, want %+v", gotResult, result)
	}
}
