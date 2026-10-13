package describe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
)

// reply is a Messages API response whose text is v as JSON.
func reply(w http.ResponseWriter, stop string, v any) {
	text, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": DefaultModel,
		"content":     []any{map[string]any{"type": "text", "text": string(text)}},
		"stop_reason": stop,
		"usage":       map[string]any{"input_tokens": 10, "output_tokens": 10},
	})
}

type summary struct {
	ID      int    `json:"id"`
	Summary string `json:"summary"`
}

// fake serves the Messages API with handler, given the decoded request
// body and the call number, and counts calls.
func fake(t *testing.T, handler func(w http.ResponseWriter, body map[string]any, call int)) (*Anthropic, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path %s", r.URL.Path)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		handler(w, body, int(calls.Add(1)))
	}))
	t.Cleanup(srv.Close)
	return NewAnthropic(DefaultModel, option.WithBaseURL(srv.URL), option.WithAPIKey("test-key"), option.WithMaxRetries(0)), &calls
}

func userText(body map[string]any) string {
	msgs := body["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	return content[0].(map[string]any)["text"].(string)
}

func TestSendsHaikuTheChunksWithAJSONSchemaAtLowEffort(t *testing.T) {
	a, _ := fake(t, func(w http.ResponseWriter, body map[string]any, _ int) {
		if body["model"] != DefaultModel {
			t.Errorf("model %v", body["model"])
		}
		cfg := body["output_config"].(map[string]any)
		if cfg["effort"] != "low" || cfg["format"].(map[string]any)["type"] != "json_schema" {
			t.Errorf("output_config %v", cfg)
		}
		if sys := body["system"].([]any)[0].(map[string]any)["text"]; sys != describePrompt {
			t.Errorf("system %v", sys)
		}
		want := "<chunk id=\"1\" path=\"src/a.go\">\nfunc a() {}\n</chunk>\n<chunk id=\"2\" path=\"src/b.go\">\nfunc b() {}\n</chunk>\n"
		if got := userText(body); got != want {
			t.Errorf("user %q", got)
		}
		reply(w, "end_turn", map[string]any{"chunks": []summary{{2, "Does b."}, {1, " Does a. "}}})
	})
	got, err := a.Describe(context.Background(), []Chunk{{"src/a.go", "func a() {}"}, {"src/b.go", "func b() {}"}})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "Does a." || got[1] != "Does b." {
		t.Fatalf("got %q", got)
	}
}

func TestAsksAgainForChunksClaudeLeftOut(t *testing.T) {
	a, calls := fake(t, func(w http.ResponseWriter, body map[string]any, call int) {
		switch call {
		case 1:
			reply(w, "end_turn", map[string]any{"chunks": []summary{{1, "Does a."}, {3, ""}}})
		case 2:
			if got := userText(body); strings.Count(got, "<chunk ") != 2 || !strings.Contains(got, `id="2" path="src/c.go"`) {
				t.Errorf("second call should hold only b and c, renumbered: %q", got)
			}
			reply(w, "end_turn", map[string]any{"chunks": []summary{{1, "Does b."}, {2, "Does c."}}})
		}
	})
	got, err := a.Describe(context.Background(), []Chunk{{"src/a.go", "a"}, {"src/b.go", "b"}, {"src/c.go", "c"}})
	if err != nil || calls.Load() != 2 || strings.Join(got, " ") != "Does a. Does b. Does c." {
		t.Fatalf("got %q, %v after %d calls", got, err, calls.Load())
	}

	a, _ = fake(t, func(w http.ResponseWriter, _ map[string]any, _ int) {
		reply(w, "end_turn", map[string]any{"chunks": []summary{}})
	})
	if _, err := a.Describe(context.Background(), []Chunk{{"src/a.go", "a"}}); err == nil || !strings.Contains(err.Error(), "left out 1 of 1") {
		t.Fatalf("after a second miss it should give up, got %v", err)
	}
}

func TestRefusalsAndCutOffRepliesAreErrors(t *testing.T) {
	for stop, want := range map[string]string{"refusal": "declined", "max_tokens": "cut off"} {
		a, _ := fake(t, func(w http.ResponseWriter, _ map[string]any, _ int) {
			reply(w, stop, map[string]any{"chunks": []summary{{1, "Does a."}}})
		})
		if _, err := a.Describe(context.Background(), []Chunk{{"src/a.go", "a"}}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", stop, err)
		}
	}
}

func TestPhrasesAFeatureOrADraft(t *testing.T) {
	a, _ := fake(t, func(w http.ResponseWriter, body map[string]any, call int) {
		want := map[int]string{1: "Feature: writes matches as CSV", 2: "<draft path=\"draft.go\">\nfunc writeCSV() {}\n</draft>"}[call]
		if got := userText(body); got != want {
			t.Errorf("call %d: %q", call, got)
		}
		reply(w, "end_turn", map[string]any{"does": "writes matches as csv", "broad": " exports results to an output format "})
	})
	q, err := a.Phrase(context.Background(), Plan{Feature: "writes matches as CSV"})
	if err != nil || q.Broad != "exports results to an output format" {
		t.Fatalf("got %+v, %v", q, err)
	}
	if _, err := a.Phrase(context.Background(), Plan{Draft: Chunk{"draft.go", "func writeCSV() {}"}}); err != nil {
		t.Fatal(err)
	}
}

// TestRealAPISmoke calls Claude for real. It runs only with an API key set.
func TestRealAPISmoke(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	a := NewAnthropic(DefaultModel)
	got, err := a.Describe(context.Background(), []Chunk{
		{"billing/retry.go", "func RetryFailedPayment(inv Invoice) error {\n\tfor attempt := 1; attempt <= 3; attempt++ {\n\t\tif err := charge(inv); err == nil {\n\t\t\treturn nil\n\t\t}\n\t\ttime.Sleep(time.Duration(attempt) * time.Second)\n\t}\n\treturn ErrGaveUp\n}"},
		{"mail/receipt.go", "func SendReceipt(to string, inv Invoice) error {\n\treturn smtp.SendMail(addr, nil, from, []string{to}, render(inv))\n}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(got[0]), "retr") || !strings.Contains(strings.ToLower(got[1]), "receipt") {
		t.Fatalf("got %q", got)
	}
	q, err := a.Phrase(context.Background(), Plan{Feature: "writes invoices out as CSV"})
	if err != nil || q.Broad == "" {
		t.Fatalf("got %+v, %v", q, err)
	}
	fmt.Println(got, q)
}
