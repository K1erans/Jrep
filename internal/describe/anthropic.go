package describe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Anthropic is the Client for Claude's API.
type Anthropic struct {
	client anthropic.Client
	model  string
}

// NewAnthropic logs in the way the Anthropic SDK does: ANTHROPIC_API_KEY,
// then ANTHROPIC_AUTH_TOKEN, then the profile named by ANTHROPIC_PROFILE,
// then workload identity federation, then the active profile from `ant auth
// login`. With none of them, the first call fails with an error listing
// each. opts come after the defaults, so tests can point it elsewhere.
func NewAnthropic(model string, opts ...option.RequestOption) *Anthropic {
	opts = append([]option.RequestOption{option.WithMaxRetries(6)}, opts...)
	return &Anthropic{client: anthropic.NewClient(opts...), model: model}
}

func (a *Anthropic) Model() string { return a.model }

func (a *Anthropic) Describe(ctx context.Context, chunks []Chunk) ([]string, error) {
	return describeWith(ctx, a.call, chunks)
}

func (a *Anthropic) Phrase(ctx context.Context, plan Plan) (Questions, error) {
	return phraseWith(ctx, a.call, plan)
}

// call is the API's ask: one Messages call.
func (a *Anthropic) call(ctx context.Context, system, user string, schema map[string]any, out any) error {
	msg, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     a.model,
		MaxTokens: 16000,
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(user))},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffortLow,
			Format: anthropic.JSONOutputFormatParam{Schema: schema},
		},
	})
	if err != nil {
		return err
	}
	switch msg.StopReason {
	case anthropic.StopReasonRefusal:
		return errors.New("Claude declined to describe this code")
	case anthropic.StopReasonMaxTokens:
		return errors.New("Claude's reply was cut off at its token limit")
	}
	var text strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	if err := json.Unmarshal([]byte(text.String()), out); err != nil {
		return fmt.Errorf("reading Claude's reply: %w", err)
	}
	return nil
}
