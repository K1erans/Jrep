package describe

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeClaude writes a claude command into dir that saves its arguments
// and stdin there, prints reply and exits with status.
func fakeClaude(t *testing.T, dir, reply string, status int) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "reply"), []byte(reply), 0o644); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nd=$(dirname \"$0\")\nprintf '%%s\\0' \"$@\" > \"$d/args\"\ncat > \"$d/stdin\"\ncat \"$d/reply\"\nexit %d\n", status)
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func saved(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestClaudeCodeSendsThePromptWithNoToolsAndASchema(t *testing.T) {
	dir := t.TempDir()
	out, _ := json.Marshal(map[string]any{"is_error": false, "subtype": "success",
		"structured_output": map[string]any{"chunks": []summary{{1, "adds"}, {2, "subtracts"}}}})
	c := NewClaudeCode(fakeClaude(t, dir, string(out), 0), DefaultModel)

	got, err := c.Describe(context.Background(), []Chunk{{"a.go", "func add() {}"}, {"b.go", "func sub() {}"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"adds", "subtracts"}) {
		t.Fatalf("got %q", got)
	}
	args := strings.Split(strings.TrimSuffix(saved(t, dir, "args"), "\x00"), "\x00")
	flag := func(name string) string {
		i := slices.Index(args, name)
		if i < 0 || i+1 == len(args) {
			t.Fatalf("no %s in %q", name, args)
		}
		return args[i+1]
	}
	if args[0] != "-p" || flag("--model") != DefaultModel || flag("--effort") != "low" ||
		flag("--system-prompt") != describePrompt || flag("--tools") != "" || flag("--output-format") != "json" {
		t.Fatalf("args %q", args)
	}
	for _, f := range []string{"--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence"} {
		if !slices.Contains(args, f) {
			t.Fatalf("no %s in %q", f, args)
		}
	}
	if !json.Valid([]byte(flag("--json-schema"))) {
		t.Fatalf("schema %q", flag("--json-schema"))
	}
	if stdin := saved(t, dir, "stdin"); !strings.Contains(stdin, "<chunk id=\"2\" path=\"b.go\">\nfunc sub() {}\n</chunk>") {
		t.Fatalf("stdin %q", stdin)
	}
}

func TestClaudeCodeErrorsSayWhatWentWrong(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		want        string
	}{
		{"not logged in", `{"is_error":true,"subtype":"success","result":"Invalid API key · Please run /login"}`, "Please run /login"},
		{"no structured reply", `{"is_error":false,"subtype":"error_max_structured_output_retries"}`, "error_max_structured_output_retries"},
		{"crashed", "", "exit status 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClaudeCode(fakeClaude(t, t.TempDir(), tc.reply, 1), DefaultModel)
			_, err := c.Phrase(context.Background(), Plan{Feature: "writes CSV"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoginPicksTheAPIOrClaudeCode(t *testing.T) {
	withClaude := t.TempDir()
	fakeClaude(t, withClaude, "", 0)
	without := t.TempDir()
	for _, tc := range []struct {
		name, login, key, path string
		want                   string // the Client's type, or the error
	}{
		{"an API key wins", "", "sk-test", withClaude, "*describe.Anthropic"},
		{"else Claude Code", "", "", withClaude, "*describe.ClaudeCode"},
		{"else the API, to say how to log in", "", "", without, "*describe.Anthropic"},
		{"chosen: api", "api", "", withClaude, "*describe.Anthropic"},
		{"chosen: claude", "claude", "sk-test", withClaude, "*describe.ClaudeCode"},
		{"chosen but missing", "claude", "", without, "Claude Code isn't installed"},
		{"unknown", "web", "", withClaude, "must be api or claude"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, v := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_PROFILE", "ANTHROPIC_FEDERATION_RULE_ID"} {
				t.Setenv(v, "")
			}
			t.Setenv("ANTHROPIC_CONFIG_DIR", t.TempDir())
			t.Setenv("ANTHROPIC_API_KEY", tc.key)
			t.Setenv("JREP_CLAUDE_LOGIN", tc.login)
			t.Setenv("PATH", tc.path)
			c, err := Login(DefaultModel)
			got := fmt.Sprintf("%T", c)
			if err != nil {
				got = err.Error()
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// TestClaudeCodeSmoke describes a chunk with the real claude command. It
// runs with JREP_CLAUDE_SMOKE=1, using Claude Code's login.
func TestClaudeCodeSmoke(t *testing.T) {
	if os.Getenv("JREP_CLAUDE_SMOKE") == "" {
		t.Skip("set JREP_CLAUDE_SMOKE=1 to call Claude through the claude command")
	}
	t.Setenv("JREP_CLAUDE_LOGIN", "claude")
	c, err := Login(DefaultModel)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Describe(context.Background(), []Chunk{{"retry.go", "func retry(f func() error) error {\n\tfor i := 0; ; i++ {\n\t\tif err := f(); err == nil || i == 3 {\n\t\t\treturn err\n\t\t}\n\t\ttime.Sleep(time.Second << i)\n\t}\n}"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Log(got[0])
	if len(got) != 1 || !strings.Contains(strings.ToLower(got[0]), "retr") {
		t.Fatalf("got %q", got)
	}
}
