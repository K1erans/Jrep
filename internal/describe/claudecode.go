package describe

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ClaudeCode is the Client that runs Claude Code's claude command, so it
// uses Claude Code's login, such as a Claude subscription.
type ClaudeCode struct {
	path, model string
}

// NewClaudeCode runs the claude command at path.
func NewClaudeCode(path, model string) *ClaudeCode {
	return &ClaudeCode{path: path, model: model}
}

func (c *ClaudeCode) Model() string { return c.model }

func (c *ClaudeCode) Describe(ctx context.Context, chunks []Chunk) ([]string, error) {
	return describeWith(ctx, c.call, chunks)
}

func (c *ClaudeCode) Phrase(ctx context.Context, plan Plan) (Questions, error) {
	return phraseWith(ctx, c.call, plan)
}

// call is Claude Code's ask: one claude -p run with no tools, MCP servers,
// settings or skills, so Claude sees little but the prompt. It runs in the
// temp folder, away from any project's CLAUDE.md.
func (c *ClaudeCode) call(ctx context.Context, system, user string, schema map[string]any, out any) error {
	s, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, c.path, "-p",
		"--model", c.model,
		"--effort", "low",
		"--system-prompt", system,
		"--tools", "",
		"--strict-mcp-config",
		"--setting-sources", "",
		"--disable-slash-commands",
		"--no-session-persistence",
		"--output-format", "json",
		"--json-schema", string(s))
	cmd.Stdin = strings.NewReader(user)
	cmd.Dir = os.TempDir()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, runErr := cmd.Output()
	var reply struct {
		IsError          bool            `json:"is_error"`
		Subtype          string          `json:"subtype"`
		Result           string          `json:"result"`
		StructuredOutput json.RawMessage `json:"structured_output"`
	}
	if err := json.Unmarshal(stdout, &reply); err != nil {
		if runErr != nil {
			return fmt.Errorf("claude: %w: %s", runErr, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("reading claude's reply: %w", err)
	}
	if reply.IsError || len(reply.StructuredOutput) == 0 || string(reply.StructuredOutput) == "null" {
		return fmt.Errorf("claude: %s", cmp.Or(reply.Result, reply.Subtype))
	}
	if err := json.Unmarshal(reply.StructuredOutput, out); err != nil {
		return fmt.Errorf("reading claude's reply: %w", err)
	}
	return nil
}
