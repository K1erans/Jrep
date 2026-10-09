package search

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"jrep/internal/cache"
	"jrep/internal/chunk"
	"jrep/internal/jev"
	"jrep/internal/jev/jevtest"
)

func chunks() []Chunk {
	return []Chunk{
		{Path: "src/retry.ts", Rel: "app/src/retry.ts", Chunk: chunk.Chunk{Start: 12, End: 80, Signature: "retryFailedPayment(invoice)", Code: "retry payment"}},
		{Path: "src/worker.ts", Rel: "app/src/worker.ts", Chunk: chunk.Chunk{Start: 40, End: 95, Signature: "handleFailure(job)", Code: "worker payment email"}},
		{Path: "src/http.ts", Rel: "app/src/http.ts", Chunk: chunk.Chunk{Start: 5, End: 40, Signature: "get(url)", Code: "http"}},
	}
}

// scores gives fixed probabilities per path and question.
func scores(table map[string]map[string]float64) func(jev.State, string) float64 {
	return func(s jev.State, q string) float64 { return table[q][s.Path] }
}

var paymentScores = map[string]map[string]float64{
	"retries failed payments": {"app/src/retry.ts": 0.94, "app/src/worker.ts": 0.81, "app/src/http.ts": 0.21},
	"sends email":             {"app/src/retry.ts": 0.05, "app/src/worker.ts": 0.70, "app/src/http.ts": 0.10},
}

func opts(questions ...string) Options {
	return Options{Questions: questions, Model: "jev-1.13.0", Threshold: 0.6, Top: 10, ReadCache: true}
}

func TestRanksByProbabilityAndAppliesThresholdAndTop(t *testing.T) {
	fake := &jevtest.Fake{Score: scores(paymentScores)}
	res, err := Run(context.Background(), chunks(), fake, cache.NewMemory(), opts("retries failed payments"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Match{
		{Path: "src/retry.ts", Start: 12, End: 80, Signature: "retryFailedPayment(invoice)", Probability: 0.94},
		{Path: "src/worker.ts", Start: 40, End: 95, Signature: "handleFailure(job)", Probability: 0.81},
	}
	if !reflect.DeepEqual(res[0].Matches, want) {
		t.Fatalf("got %+v", res[0].Matches)
	}

	o := opts("retries failed payments")
	o.Top = 1
	res, _ = Run(context.Background(), chunks(), fake, cache.NewMemory(), o)
	if len(res[0].Matches) != 1 || res[0].Matches[0].Path != "src/retry.ts" {
		t.Fatalf("top 1: %+v", res[0].Matches)
	}
}

func TestNoMatchStillReportsTheBestCandidate(t *testing.T) {
	fake := &jevtest.Fake{Score: scores(paymentScores)}
	o := opts("retries failed payments")
	o.Threshold = 0.95
	res, err := Run(context.Background(), chunks(), fake, cache.NewMemory(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res[0].Matches) != 0 {
		t.Fatalf("expected no matches, got %+v", res[0].Matches)
	}
	if res[0].Best == nil || res[0].Best.Path != "src/retry.ts" || res[0].Best.Probability != 0.94 {
		t.Fatalf("best: %+v", res[0].Best)
	}
}

func TestAllQuestionsShareOneRequestPerChunk(t *testing.T) {
	fake := &jevtest.Fake{Score: scores(paymentScores)}
	res, err := Run(context.Background(), chunks(), fake, cache.NewMemory(), opts("retries failed payments", "sends email"))
	if err != nil {
		t.Fatal(err)
	}
	reqs := fake.Requests()
	if len(reqs) != 3 {
		t.Fatalf("want one request per chunk, got %d", len(reqs))
	}
	for _, r := range reqs {
		if len(r.Questions) != 2 || r.Model != "jev-1.13.0" {
			t.Fatalf("request %+v", r)
		}
		if r.State.Path == "app/src/retry.ts" && r.State.Code != "retry payment" {
			t.Fatalf("state should carry the repo path and code: %+v", r.State)
		}
	}
	if res[1].Question != "sends email" || len(res[1].Matches) != 1 || res[1].Matches[0].Path != "src/worker.ts" {
		t.Fatalf("second question: %+v", res[1])
	}
}

func TestCachedAnswersAreReusedAcrossThresholds(t *testing.T) {
	store := cache.NewMemory()
	first := &jevtest.Fake{Score: scores(paymentScores)}
	if _, err := Run(context.Background(), chunks(), first, store, opts("retries failed payments")); err != nil {
		t.Fatal(err)
	}

	again := &jevtest.Fake{Score: scores(paymentScores)}
	o := opts("Retries  FAILED payments")
	o.Threshold = 0.2
	res, err := Run(context.Background(), chunks(), again, store, o)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(again.Requests()); n != 0 {
		t.Fatalf("a cached search should make no calls, made %d", n)
	}
	if len(res[0].Matches) != 3 {
		t.Fatalf("lower threshold from cache: %+v", res[0].Matches)
	}

	o.ReadCache = false
	if _, err := Run(context.Background(), chunks(), again, store, o); err != nil {
		t.Fatal(err)
	}
	if n := len(again.Requests()); n != 3 {
		t.Fatalf("without reading the cache every chunk is asked, got %d", n)
	}
}

func TestOnlyUncachedQuestionsAreAsked(t *testing.T) {
	store := cache.NewMemory()
	fake := &jevtest.Fake{Score: scores(paymentScores)}
	if _, err := Run(context.Background(), chunks(), fake, store, opts("retries failed payments")); err != nil {
		t.Fatal(err)
	}
	fake2 := &jevtest.Fake{Score: scores(paymentScores)}
	if _, err := Run(context.Background(), chunks(), fake2, store, opts("retries failed payments", "sends email")); err != nil {
		t.Fatal(err)
	}
	for _, r := range fake2.Requests() {
		if !reflect.DeepEqual(r.Questions, []string{jev.Instruction("sends email")}) {
			t.Fatalf("only the new question should be sent, got %q", r.Questions)
		}
	}
}

type failOn struct {
	jev.Client
	path string
}

func (f failOn) Ask(ctx context.Context, r jev.Request) (jev.Response, error) {
	if r.State.Path == f.path {
		return jev.Response{}, errors.New("boom")
	}
	return f.Client.Ask(ctx, r)
}

func TestAFailedCallStopsTheSearchButKeepsEarlierAnswers(t *testing.T) {
	store := cache.NewMemory()
	client := failOn{Client: &jevtest.Fake{Score: scores(paymentScores)}, path: "app/src/http.ts"}
	o := opts("retries failed payments")
	o.Workers = 1
	_, err := Run(context.Background(), chunks(), client, store, o)
	if err == nil {
		t.Fatal("expected the error")
	}

	fake := &jevtest.Fake{Score: scores(paymentScores)}
	if _, err := Run(context.Background(), chunks(), fake, store, o); err != nil {
		t.Fatal(err)
	}
	if n := len(fake.Requests()); n != 1 {
		t.Fatalf("answers saved before the failure should be reused; made %d calls", n)
	}
}

func TestDuplicateQuestionsAreAskedOnce(t *testing.T) {
	fake := &jevtest.Fake{Score: scores(paymentScores)}
	res, err := Run(context.Background(), chunks(), fake, cache.NewMemory(), opts("sends email", "sends  email"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || len(fake.Requests()[0].Questions) != 1 {
		t.Fatalf("got %d results, %d questions", len(res), len(fake.Requests()[0].Questions))
	}
}

func TestAnswersSurviveCodeMovingWithinItsFile(t *testing.T) {
	store := cache.NewMemory()
	if _, err := Run(context.Background(), chunks(), &jevtest.Fake{Score: scores(paymentScores)}, store, opts("retries failed payments")); err != nil {
		t.Fatal(err)
	}
	moved := chunks()
	for i := range moved {
		moved[i].Start += 7 // lines added above every chunk
		moved[i].End += 7
	}
	again := &jevtest.Fake{Score: scores(paymentScores)}
	res, err := Run(context.Background(), moved, again, store, opts("retries failed payments"))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(again.Requests()); n != 0 {
		t.Fatalf("unchanged code that only moved should come from the cache, made %d calls", n)
	}
	if m := res[0].Matches[0]; m.Start != 19 || m.End != 87 {
		t.Fatalf("results show the new lines, got %d-%d", m.Start, m.End)
	}
}
