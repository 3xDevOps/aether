package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestParseWorkspaceEnvSet(t *testing.T) {
	workspace, got, secret, err := parseWorkspaceEnvSet([]string{"--workspace", "app", "NODE_ENV=test", "API_URL=https://api.example.test/?a=b", "EMPTY="})
	want := []protocol.WorkspaceVariable{
		{Name: "NODE_ENV", Value: "test"},
		{Name: "API_URL", Value: "https://api.example.test/?a=b"},
		{Name: "EMPTY"},
	}
	if err != nil || workspace != "app" || secret != "" || !slices.Equal(got, want) {
		t.Fatalf("plain set = (%q, %+v, %q, %v), want (app, %+v, no secret, nil)", workspace, got, secret, err, want)
	}

	if _, got, secret, err = parseWorkspaceEnvSet([]string{"--secret", "NPM_TOKEN"}); err != nil || secret != "NPM_TOKEN" || got != nil {
		t.Fatalf("secret set = (%+v, %q, %v), want only the name NPM_TOKEN", got, secret, err)
	}

	for name, args := range map[string][]string{
		"no variable":                    {},
		"no value":                       {"NODE_ENV"},
		"no name":                        {"=value"},
		"secret value on the command":    {"--secret", "NPM_TOKEN=token-on-argv"},
		"two secrets from one stdin":     {"--secret", "A", "B"},
		"flag after the first variable":  {"NODE_ENV=test", "--secret"},
		"unknown flag before a variable": {"--value", "x", "NODE_ENV=test"},
	} {
		_, _, _, err := parseWorkspaceEnvSet(args)
		if err == nil {
			t.Errorf("%s: parseWorkspaceEnvSet(%v) succeeded", name, args)
		} else if strings.Contains(err.Error(), "token-on-argv") {
			t.Errorf("%s: the refusal repeats the value: %v", name, err)
		}
	}
}

func TestReadSecretValueFromAPipe(t *testing.T) {
	// A piped value loses the newline the shell adds and nothing else.
	for input, want := range map[string]string{"  token value \n": "  token value ", "token\r\n": "token", "two\nlines\n": "two\nlines"} {
		if got, err := readSecretValue("NPM_TOKEN", strings.NewReader(input)); err != nil || got != want {
			t.Errorf("readSecretValue(%q) = %q, %v, want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"", "\n"} {
		if _, err := readSecretValue("NPM_TOKEN", strings.NewReader(input)); err == nil {
			t.Errorf("readSecretValue(%q) accepted an empty secret", input)
		}
	}
}

func TestParseWorkspaceEnvImport(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(file, []byte("DATABASE_URL=postgres://db.example.test/app\nNPM_TOKEN=abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, got, err := parseWorkspaceEnvImport([]string{file}, nil)
	want := []protocol.WorkspaceVariable{
		{Name: "DATABASE_URL", Value: "postgres://db.example.test/app", Secret: true},
		{Name: "NPM_TOKEN", Value: "abc", Secret: true},
	}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("import = (%+v, %v), want secrets %+v", got, err, want)
	}
	if _, got, err = parseWorkspaceEnvImport([]string{"--plain", "-"}, strings.NewReader("A=1\n")); err != nil ||
		!slices.Equal(got, []protocol.WorkspaceVariable{{Name: "A", Value: "1"}}) {
		t.Fatalf("plain import from stdin = (%+v, %v)", got, err)
	}

	_, _, err = parseWorkspaceEnvImport([]string{"-"}, strings.NewReader("A=1\nthis line holds hunter2\n"))
	if err == nil || !strings.Contains(err.Error(), "line 2") || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("malformed import = %v, want the line number and not the line", err)
	}
	if _, _, err = parseWorkspaceEnvImport([]string{"-"}, strings.NewReader("# only a comment\n")); err == nil {
		t.Fatal("an import with no variables succeeded")
	}
}

// web/src/lib/dotenv.test.ts holds the same cases for the dashboard's parser.
func TestParseDotenv(t *testing.T) {
	text := strings.Join([]string{
		"# a comment",
		"",
		"PLAIN=value",
		"export EXPORTED=yes",
		"  SPACED = padded value  ",
		`DOUBLE="two\nlines and a \"quote\""`,
		`SINGLE='kept $as \n written'`,
		"COMMENTED=value # trailing comment",
		"HASH=abc#def",
		"URL=https://example.test/?a=b",
		"EMPTY=",
		"PLAIN=last wins",
		"no equals sign",
		"=no name",
		"TWO WORDS=x",
	}, "\r\n")
	variables, badLines := parseDotenv(text)
	want := []protocol.WorkspaceVariable{
		{Name: "PLAIN", Value: "last wins"},
		{Name: "EXPORTED", Value: "yes"},
		{Name: "SPACED", Value: "padded value"},
		{Name: "DOUBLE", Value: "two\nlines and a \"quote\""},
		{Name: "SINGLE", Value: `kept $as \n written`},
		{Name: "COMMENTED", Value: "value"},
		{Name: "HASH", Value: "abc#def"},
		{Name: "URL", Value: "https://example.test/?a=b"},
		{Name: "EMPTY"},
	}
	if !slices.Equal(variables, want) {
		t.Errorf("variables = %+v\nwant %+v", variables, want)
	}
	if !slices.Equal(badLines, []int{13, 14, 15}) {
		t.Errorf("badLines = %v, want [13 14 15]", badLines)
	}
}

func TestSetupOutputComesFromALaunchRefusal(t *testing.T) {
	refusal := &protocol.Error{
		Code:    protocol.CodeInternal,
		Message: "provisioning: start container: runtime: setup script exited 1",
		Data:    []byte(`{"setup_output":"npm error code E401\n"}`),
	}
	if got := setupOutput(fmt.Errorf("launch: %w", refusal)); got != "npm error code E401\n" {
		t.Errorf("setupOutput = %q", got)
	}
	for _, err := range []error{errors.New("plain"), &protocol.Error{Message: "no data"}, &protocol.Error{Data: []byte(`{"accepted_commit":"abc"}`)}} {
		if got := setupOutput(err); got != "" {
			t.Errorf("setupOutput(%v) = %q, want none", err, got)
		}
	}
}

func TestPrintWorkspaceVariablesHidesNothingButSecretValues(t *testing.T) {
	var b strings.Builder
	ws := protocol.Workspace{ID: "ws-1", Name: "app"}
	if err := printWorkspaceVariables(&b, ws, protocol.WorkspaceEnvironmentResult{Variables: []protocol.WorkspaceVariable{
		{Name: "NODE_ENV", Value: "test"},
		{Name: "NPM_TOKEN", Secret: true},
	}}); err != nil {
		t.Fatal(err)
	}
	if want := "workspace ws-1 app\nNODE_ENV=test\nNPM_TOKEN (secret)\n"; b.String() != want {
		t.Errorf("output = %q, want %q", b.String(), want)
	}
	b.Reset()
	if err := printWorkspaceVariables(&b, ws, protocol.WorkspaceEnvironmentResult{}); err != nil || b.String() != "workspace ws-1 app\nno variables\n" {
		t.Errorf("empty output = %q, %v", b.String(), err)
	}
}
