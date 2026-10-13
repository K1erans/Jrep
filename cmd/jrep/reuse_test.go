package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"jrep/internal/describe"
	"jrep/internal/jev"
	"jrep/internal/jev/jevtest"
)

// phrases has Claude ask does and broad for every plan.
func phrases(does, broad string) func(describe.Plan) describe.Questions {
	return func(describe.Plan) describe.Questions { return describe.Questions{Does: does, Broad: broad} }
}

// byPath scores each question by the chunk's path.
func byPath(table map[string]map[string]float64) *jevtest.Fake {
	return &jevtest.Fake{Score: func(s jev.State, q string) float64 { return table[q][s.Path] }}
}

func TestReuseExtendsCodeThatDoesIt(t *testing.T) {
	project(t, billing)
	h := newHarness(scorer())
	if code := h.run("reuse", "retries failed payments"); code != exitMatch {
		t.Fatalf("exit %d, stderr %s", code, h.errOut.String())
	}
	if want := "extend  0.90  src/billing/retry.go:1-6  func RetryFailedPayment(id string) error\n"; h.out.String() != want {
		t.Fatalf("got %q", h.out.String())
	}
	want := "jrep: also asking for code that retries failed payments\njrep: describing 2 chunks with fake\njrep: asking Jev about 2 chunks\n"
	if h.errOut.String() != want {
		t.Fatalf("stderr %q", h.errOut.String())
	}
}

func TestReuseExtendsCodeThatDoesTheBroaderWork(t *testing.T) {
	project(t, billing)
	h := newHarness(byPath(map[string]map[string]float64{
		"issues refunds": {"src/billing/retry.go": 0.3},
		"charges cards":  {"src/billing/retry.go": 0.8, "src/mail/send.go": 0.1},
	}))
	h.claude.Phrases = phrases("issues refunds", "charges cards")
	if code := h.run("reuse", "issues refunds"); code != exitMatch || !strings.HasPrefix(h.out.String(), "extend  0.80  src/billing/retry.go:1-6") {
		t.Fatalf("exit %d, got %q", code, h.out.String())
	}
	if !strings.Contains(h.errOut.String(), "also asking for code that charges cards") {
		t.Fatalf("stderr %q", h.errOut.String())
	}
}

// Code that does the thing itself is closer evidence than code that does
// the broader work, even when the broader question scores higher.
func TestReusePrefersCodeThatDoesItOverBroaderWork(t *testing.T) {
	project(t, billing)
	h := newHarness(byPath(map[string]map[string]float64{
		"emails receipts":   {"src/mail/send.go": 0.7},
		"sends any message": {"src/billing/retry.go": 0.95, "src/mail/send.go": 0.9},
	}))
	h.claude.Phrases = phrases("emails receipts", "sends any message")
	if code := h.run("reuse", "emails receipts"); code != exitMatch || !strings.HasPrefix(h.out.String(), "extend  0.70  src/mail/send.go:1-5") {
		t.Fatalf("exit %d, got %q", code, h.out.String())
	}
}

func TestReuseCreatesWhenNothingFits(t *testing.T) {
	project(t, billing)
	h := newHarness(scorer())
	if code := h.run("reuse", "parses yaml", "src"); code != exitNoMatch {
		t.Fatalf("exit %d, stderr %s", code, h.errOut.String())
	}
	if want := "create  no code to extend above 0.60 (best: 0.10 src/billing/retry.go:1-6)\n"; h.out.String() != want {
		t.Fatalf("got %q", h.out.String())
	}
}

// Jev is asked about Claude's descriptions. Both see the path Jev always
// has, relative to the repo (outside git, to the search path).
func TestReuseAsksJevAboutDescriptions(t *testing.T) {
	project(t, billing)
	fake := scorer()
	h := newHarness(fake)
	h.claude.Summary = func(c describe.Chunk) string { return "Summary of " + c.Path }
	h.run("reuse", "retries failed payments")
	for _, b := range h.claude.Batches() {
		for _, c := range b {
			if c.Path != "src/billing/retry.go" && c.Path != "src/mail/send.go" {
				t.Fatalf("Claude should see the path Jev does, got %q", c.Path)
			}
		}
	}
	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	for _, r := range reqs {
		if r.Chunks[0].Code != "Summary of "+r.Path {
			t.Fatalf("request %+v", r)
		}
	}
}

func TestReuseIsAnsweredFromTheCacheTheSecondTime(t *testing.T) {
	project(t, billing)
	fake := scorer()
	h := newHarness(fake)
	h.run("reuse", "retries failed payments")
	plans, batches, reqs := len(h.claude.Plans()), len(h.claude.Batches()), len(fake.Requests())
	h.claude.Err = errors.New("no Anthropic credentials found")
	if code := h.run("reuse", "retries failed payments"); code != exitMatch {
		t.Fatalf("exit %d, stderr %s", code, h.errOut.String())
	}
	if len(h.claude.Plans()) != plans || len(h.claude.Batches()) != batches || len(fake.Requests()) != reqs {
		t.Fatal("a repeat should need neither Claude nor Jev")
	}
	if strings.Contains(h.errOut.String(), "describing") || strings.Contains(h.errOut.String(), "asking Jev") {
		t.Fatalf("stderr %q", h.errOut.String())
	}
}

func TestReuseChecksADraftFromAFileOrStdin(t *testing.T) {
	project(t, billing)
	os.WriteFile("draft.go", []byte("func chargeAgain() { charge() }\n"), 0o644)
	h := newHarness(scorer())
	h.claude.Phrases = phrases("retries failed payments", "charges cards")
	if code := h.run("reuse", "--code", "draft.go", "src"); code != exitMatch {
		t.Fatalf("exit %d, stderr %s", code, h.errOut.String())
	}
	plan := h.claude.Plans()[0]
	if plan.Feature != "" || plan.Draft.Path != "draft.go" || plan.Draft.Code != "func chargeAgain() { charge() }\n" {
		t.Fatalf("plan %+v", plan)
	}
	if !strings.HasPrefix(h.errOut.String(), "jrep: the draft retries failed payments\n") {
		t.Fatalf("stderr %q", h.errOut.String())
	}

	h.stdin = strings.NewReader("func chargeTwice() {}")
	h.run("reuse", "--code", "-", "src")
	if plan := h.claude.Plans()[1]; plan.Draft.Path != "draft" || plan.Draft.Code != "func chargeTwice() {}" {
		t.Fatalf("plan %+v", plan)
	}

	h.stdin = strings.NewReader("  \n")
	if code := h.run("reuse", "--code", "-"); code != exitError || !strings.Contains(h.errOut.String(), "draft is empty") {
		t.Fatalf("exit %d, stderr %q", code, h.errOut.String())
	}
	if code := h.run("reuse", "--code", "missing.go"); code != exitError || !strings.Contains(h.errOut.String(), "missing.go") {
		t.Fatalf("exit %d, stderr %q", code, h.errOut.String())
	}
}

func TestReuseClaudeErrorExitsTwo(t *testing.T) {
	project(t, billing)
	h := newHarness(scorer())
	h.claude.Err = errors.New("no Anthropic credentials found")
	if code := h.run("reuse", "retries failed payments"); code != exitError {
		t.Fatalf("exit %d", code)
	}
	if want := "jrep reuse: asking Claude to phrase the question: no Anthropic credentials found\n"; h.errOut.String() != want {
		t.Fatalf("stderr %q", h.errOut.String())
	}
}

func TestReuseBadFlagsExitTwoWithUsage(t *testing.T) {
	h := newHarness(scorer())
	for _, args := range [][]string{{"reuse"}, {"reuse", "-q", "x", "y"}, {"reuse", "--json", "x"}, {"reuse", "--threshold", "2", "x"}} {
		if code := h.run(args...); code != exitError || !strings.Contains(h.errOut.String(), "usage: jrep") {
			t.Fatalf("%q: exit %d, stderr %q", args, code, h.errOut.String())
		}
	}
	if code := h.run("reuse", "-h"); code != exitMatch || !strings.Contains(h.out.String(), "jrep reuse") {
		t.Fatalf("help: exit %d", code)
	}
}
