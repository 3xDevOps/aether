package jsonc

import (
	"encoding/json"
	"testing"
)

func TestNormalizePreservesStringsAndRejectsUnclosedComments(t *testing.T) {
	data := []byte("{\"url\":\"https://example.test/*not a comment*/\",/*actual*/\"values\":[\"a,}\",],}")
	var doc struct {
		URL    string   `json:"url"`
		Values []string `json:"values"`
	}
	if err := json.Unmarshal(Normalize(data), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.URL != "https://example.test/*not a comment*/" || len(doc.Values) != 1 || doc.Values[0] != "a,}" {
		t.Fatalf("quoted values changed: %+v", doc)
	}
	if json.Valid(Normalize([]byte(`{"disableAllHooks":true} /* never closed`))) {
		t.Fatal("unterminated comment accepted")
	}
}

func TestNormalizeEscapedStringsAndLineComments(t *testing.T) {
	input := "{\n" + `// line comment` + "\n" + `"text":"quote: \" // keep /* keep */ \\","values":[1,/* block */2,],` + "\n}"
	data := []byte(input)
	var got struct {
		Text   string `json:"text"`
		Values []int  `json:"values"`
	}
	if err := json.Unmarshal(Normalize(data), &got); err != nil {
		t.Fatal(err)
	}
	if got.Text != `quote: " // keep /* keep */ \` || len(got.Values) != 2 || got.Values[0] != 1 || got.Values[1] != 2 {
		t.Fatalf("escaped data changed: %+v", got)
	}
	if string(data) != input {
		t.Fatal("normalization modified source configuration")
	}
}
