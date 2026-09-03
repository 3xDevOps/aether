package profile

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// recommendPreviews is the inventory an agent was shown: claude with two
// categories, codex with one.
func recommendPreviews() []Preview {
	return []Preview{
		{Harness: "claude", Present: true, Categories: []Category{
			{Name: CategoryMemory, Files: 2}, {Name: CategorySkills, Files: 1},
		}},
		{Harness: "codex", Present: true, Categories: []Category{
			{Name: CategorySettings, Files: 1},
		}},
	}
}

func TestParseRecommendation(t *testing.T) {
	doc := `{"harnesses":[
		{"harness":"claude","import":true,"categories":["memory","skills"],
		 "reason":"Standing instructions and skills carry over to any machine."},
		{"harness":"codex","import":false,"categories":[],
		 "reason":"Only machine-specific settings are here."}
	]}`
	rec, err := ParseRecommendation([]byte(doc), recommendPreviews())
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Harnesses) != 2 {
		t.Fatalf("harnesses = %+v", rec.Harnesses)
	}
	claude := rec.Harnesses[0]
	if claude.Harness != "claude" || !claude.Import ||
		!slices.Equal(claude.Categories, []string{CategoryMemory, CategorySkills}) {
		t.Fatalf("claude = %+v", claude)
	}
	if codex := rec.Harnesses[1]; codex.Harness != "codex" || codex.Import || len(codex.Categories) != 0 {
		t.Fatalf("codex = %+v", codex)
	}
}

// The document drives which of the user's own directories get uploaded,
// so every deviation from the inventory is a failure, not a value to
// sanitize.
func TestParseRecommendationRejects(t *testing.T) {
	longReason := strings.Repeat("a", maxReasonRunes+1)
	tests := []struct {
		name string
		doc  string
		want string
	}{
		{"not json", `nope`, "not the expected JSON object"},
		{"not an object", `[{"harness":"claude"}]`, "not the expected JSON object"},
		{
			"unknown field",
			`{"harnesses":[{"harness":"claude","import":false,"reason":"no","confidence":0.9}]}`,
			"not the expected JSON object",
		},
		{"no harnesses", `{"harnesses":[]}`, "has no harnesses"},
		{
			"harness not in the inventory",
			`{"harnesses":[{"harness":"ghost","import":false,"reason":"no"}]}`,
			`harness "ghost": was not in the inventory`,
		},
		{
			"duplicate harness",
			`{"harnesses":[{"harness":"claude","import":false,"reason":"no"},
			  {"harness":"claude","import":false,"reason":"no again"}]}`,
			`harness "claude": appears twice`,
		},
		{
			"empty reason",
			`{"harnesses":[{"harness":"claude","import":false,"reason":"   "}]}`,
			`harness "claude": reason is empty`,
		},
		{
			"reason too long",
			fmt.Sprintf(`{"harnesses":[{"harness":"claude","import":false,"reason":%q}]}`, longReason),
			fmt.Sprintf(`harness "claude": reason is longer than %d characters`, maxReasonRunes),
		},
		{
			"multi-line reason",
			`{"harnesses":[{"harness":"claude","import":false,"reason":"one\ntwo"}]}`,
			`harness "claude": reason must be one sentence on one line`,
		},
		{
			"import without categories",
			`{"harnesses":[{"harness":"claude","import":true,"categories":[],"reason":"yes"}]}`,
			`harness "claude": import is true but no categories are listed`,
		},
		{
			"categories without import",
			`{"harnesses":[{"harness":"claude","import":false,"categories":["memory"],"reason":"no"}]}`,
			`harness "claude": import is false but categories are listed`,
		},
		{
			"category this machine does not have",
			`{"harnesses":[{"harness":"claude","import":true,"categories":["mcp"],"reason":"yes"}]}`,
			`harness "claude": category "mcp" is not one this machine has (memory, skills)`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, err := ParseRecommendation([]byte(tc.doc), recommendPreviews())
			if err == nil {
				t.Fatalf("accepted %s: %+v", tc.name, rec)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
