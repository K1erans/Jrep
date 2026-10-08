package output

import (
	"bytes"
	"testing"

	"jrep/internal/search"
)

var payments = search.Result{
	Question: "retries failed payments",
	Matches: []search.Match{
		{Path: "src/billing/retry.ts", Start: 12, End: 80, Signature: "retryFailedPayment(invoice, attempt)", Probability: 0.94},
		{Path: "src/jobs/payment-worker.ts", Start: 40, End: 95, Signature: "handlePaymentFailure(job)", Probability: 0.81},
		{Path: "src/api/webhooks.ts", Start: 110, End: 140, Probability: 0.63},
	},
}

func TestTextAlignsColumns(t *testing.T) {
	var b bytes.Buffer
	Text(&b, []search.Result{payments}, 0.6)
	want := "" +
		"0.94  src/billing/retry.ts:12-80        retryFailedPayment(invoice, attempt)\n" +
		"0.81  src/jobs/payment-worker.ts:40-95  handlePaymentFailure(job)\n" +
		"0.63  src/api/webhooks.ts:110-140\n"
	if b.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", b.String(), want)
	}
}

func TestTextSaysPlainlyWhenNothingMatched(t *testing.T) {
	best := search.Match{Path: "src/util/http.ts", Start: 5, End: 40, Probability: 0.21}
	var b bytes.Buffer
	Text(&b, []search.Result{{Question: "q", Best: &best}}, 0.6)
	if want := "no matches above 0.60 (best: 0.21 src/util/http.ts:5-40)\n"; b.String() != want {
		t.Fatalf("got %q", b.String())
	}

	b.Reset()
	Text(&b, []search.Result{{Question: "q"}}, 0.6)
	if want := "no matches above 0.60 (nothing searched)\n"; b.String() != want {
		t.Fatalf("got %q", b.String())
	}
}

func TestTextHeadsEachQuestionWhenThereAreSeveral(t *testing.T) {
	email := search.Result{Question: "sends email", Best: &search.Match{Path: "a.go", Start: 1, End: 9, Probability: 0.1}}
	var b bytes.Buffer
	Text(&b, []search.Result{{Question: payments.Question, Matches: payments.Matches[:1]}, email}, 0.6)
	want := "" +
		"# retries failed payments\n" +
		"0.94  src/billing/retry.ts:12-80  retryFailedPayment(invoice, attempt)\n" +
		"\n" +
		"# sends email\n" +
		"no matches above 0.60 (best: 0.10 a.go:1-9)\n"
	if b.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", b.String(), want)
	}
}

func TestJSONPrintsOneObjectPerMatch(t *testing.T) {
	var b bytes.Buffer
	if err := JSON(&b, []search.Result{{Question: payments.Question, Matches: payments.Matches[:2]}, {Question: "none"}}); err != nil {
		t.Fatal(err)
	}
	want := "" +
		`{"question":"retries failed payments","path":"src/billing/retry.ts","start":12,"end":80,"probability":0.94,"signature":"retryFailedPayment(invoice, attempt)"}` + "\n" +
		`{"question":"retries failed payments","path":"src/jobs/payment-worker.ts","start":40,"end":95,"probability":0.81,"signature":"handlePaymentFailure(job)"}` + "\n"
	if b.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", b.String(), want)
	}
}

func TestNoMatchesListsOnlyQuestionsWithoutMatches(t *testing.T) {
	var b bytes.Buffer
	NoMatches(&b, []search.Result{payments, {Question: "sends email"}}, 0.6)
	if want := "sends email: no matches above 0.60 (nothing searched)\n"; b.String() != want {
		t.Fatalf("got %q", b.String())
	}
}
