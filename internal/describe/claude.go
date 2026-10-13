package describe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/config"
)

// DefaultModel writes descriptions. Through the claude CLI, five calls at a
// time, Haiku described all 134 of jrep's chunks in 20 s, and Jev found the
// right chunk first by its description for all 12 features asked about.
const DefaultModel = "claude-haiku-5-5"

// The prompts are part of every cache key, so editing one describes
// everything again. Both logins send the same ones, so they share the cache.
const (
	describePrompt = `You describe source code for a search index. For each chunk, write one to three plain sentences saying what the code does: the behaviour and features it provides, in the words a developer would use when looking for it. Describe behaviour, not syntax; skip variable names unless they are the feature. Reply with every chunk's id.`

	phrasePrompt = `A coding agent is about to write new code and wants to know whether existing code already does it, or could be extended to do it, before writing anything. You will be given the new code's feature in words, or its draft. Reply with:

- does: what the new code does, as one short phrase starting with a lowercase verb, in the words a developer would use to look for it. When given a feature in words, repeat it.
- broad: the broader capability that existing code would already have if the agent should extend it instead. Drop what is new and specific, such as the format, unit, destination, service or platform, and keep the kind of work it is an instance of. One short phrase starting with a lowercase verb.`
)

var (
	describeSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"chunks": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":      map[string]any{"type": "integer"},
						"summary": map[string]any{"type": "string"},
					},
					"required":             []string{"id", "summary"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"chunks"},
		"additionalProperties": false,
	}
	phraseSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"does":  map[string]any{"type": "string"},
			"broad": map[string]any{"type": "string"},
		},
		"required":             []string{"does", "broad"},
		"additionalProperties": false,
	}
)

// Login picks how to reach Claude. JREP_CLAUDE_LOGIN=api logs in the way
// the Anthropic SDK does (see NewAnthropic); JREP_CLAUDE_LOGIN=claude runs
// Claude Code's claude command, which uses its login, such as a Claude
// subscription. Unset, it is the API when a login for it is set up, else
// the claude command when it is installed, else the API, whose first call
// then fails with an error listing how to log in.
func Login(model string) (Client, error) {
	switch login := os.Getenv("JREP_CLAUDE_LOGIN"); login {
	case "api":
		return NewAnthropic(model), nil
	case "claude":
		path, err := exec.LookPath("claude")
		if err != nil {
			return nil, fmt.Errorf("JREP_CLAUDE_LOGIN=claude, but Claude Code isn't installed: %w", err)
		}
		return NewClaudeCode(path, model), nil
	case "":
	default:
		return nil, fmt.Errorf("JREP_CLAUDE_LOGIN must be api or claude, got %q", login)
	}
	if !apiLogin() {
		if path, err := exec.LookPath("claude"); err == nil {
			return NewClaudeCode(path, model), nil
		}
	}
	return NewAnthropic(model), nil
}

// apiLogin reports whether the SDK has a login to use: one of its
// environment variables, or a profile from `ant auth login`.
func apiLogin() bool {
	for _, v := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_PROFILE", "ANTHROPIC_FEDERATION_RULE_ID"} {
		if os.Getenv(v) != "" {
			return true
		}
	}
	_, err := config.LoadConfig()
	return err == nil
}

// ask sends Claude one prompt and decodes its JSON reply, which the schema
// guarantees, into out. Each login has its own.
type ask func(ctx context.Context, system, user string, schema map[string]any, out any) error

// describeWith asks for every chunk's description in one call, then once
// more for any Claude left out.
func describeWith(ctx context.Context, ask ask, chunks []Chunk) ([]string, error) {
	out := make([]string, len(chunks))
	todo := make([]int, len(chunks))
	for i := range todo {
		todo[i] = i
	}
	for attempt := 0; len(todo) > 0; attempt++ {
		if attempt == 2 {
			return nil, fmt.Errorf("Claude left out %d of %d chunks, starting with %s", len(todo), len(chunks), chunks[todo[0]].Path)
		}
		var b strings.Builder
		for j, i := range todo {
			fmt.Fprintf(&b, "<chunk id=\"%d\" path=\"%s\">\n%s\n</chunk>\n", j+1, chunks[i].Path, chunks[i].Code)
		}
		var reply struct {
			Chunks []struct {
				ID      int    `json:"id"`
				Summary string `json:"summary"`
			} `json:"chunks"`
		}
		if err := ask(ctx, describePrompt, b.String(), describeSchema, &reply); err != nil {
			return nil, err
		}
		for _, c := range reply.Chunks {
			if c.ID >= 1 && c.ID <= len(todo) && strings.TrimSpace(c.Summary) != "" {
				out[todo[c.ID-1]] = strings.TrimSpace(c.Summary)
			}
		}
		var left []int
		for _, i := range todo {
			if out[i] == "" {
				left = append(left, i)
			}
		}
		todo = left
	}
	return out, nil
}

func phraseWith(ctx context.Context, ask ask, plan Plan) (Questions, error) {
	user := "Feature: " + plan.Feature
	if plan.Feature == "" {
		user = fmt.Sprintf("<draft path=\"%s\">\n%s\n</draft>", plan.Draft.Path, plan.Draft.Code)
	}
	var q Questions
	if err := ask(ctx, phrasePrompt, user, phraseSchema, &q); err != nil {
		return Questions{}, err
	}
	q.Does, q.Broad = strings.TrimSpace(q.Does), strings.TrimSpace(q.Broad)
	if q.Does == "" || q.Broad == "" {
		return Questions{}, errors.New("Claude returned an empty question")
	}
	return q, nil
}
