package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jrep/internal/cache"
	"jrep/internal/jev"
	"jrep/internal/jev/jevtest"
)

// project writes a small non-git source tree and moves into it.
func project(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
}

var billing = map[string]string{
	"src/billing/retry.go": "package billing\n\n// RetryFailedPayment charges again.\nfunc RetryFailedPayment(id string) error {\n\treturn charge(id)\n}\n",
	"src/mail/send.go":     "package mail\n\nfunc SendReceipt(to string) error {\n\treturn smtp(to)\n}\n",
}

type harness struct {
	app
	out, errOut bytes.Buffer
	store       *cache.Memory
}

func newHarness(client jev.Client) *harness {
	h := &harness{store: cache.NewMemory()}
	h.app = app{
		stdout: &h.out,
		stderr: &h.errOut,
		client: func(string) jev.Client { return client },
		openCache: func(string, string) (cache.Cache, func() error, error) {
			return h.store, func() error { return nil }, nil
		},
		clearCache: func(string) error { return nil },
	}
	return h
}

func (h *harness) run(args ...string) int {
	h.out.Reset()
	h.errOut.Reset()
	return h.app.run(context.Background(), args)
}

func scorer() *jevtest.Fake {
	return &jevtest.Fake{Score: jevtest.Contains(map[string]string{
		"retries failed payments": "charge",
		"sends email":             "smtp",
		"parses yaml":             "yaml",
	})}
}

func TestMatchPrintsRangesAndExitsZero(t *testing.T) {
	project(t, billing)
	h := newHarness(scorer())
	if code := h.run("retries failed payments"); code != exitMatch {
		t.Fatalf("exit %d, stderr %s", code, h.errOut.String())
	}
	want := "0.90  src/billing/retry.go:1-6  func RetryFailedPayment(id string) error\n"
	if h.out.String() != want {
		t.Fatalf("got %q\nwant %q", h.out.String(), want)
	}
}

func TestNoMatchExitsOneAndSaysSo(t *testing.T) {
	project(t, billing)
	h := newHarness(scorer())
	if code := h.run("parses yaml", "src"); code != exitNoMatch {
		t.Fatalf("exit %d", code)
	}
	if want := "no matches above 0.60 (best: 0.10 src/billing/retry.go:1-6)\n"; h.out.String() != want {
		t.Fatalf("got %q", h.out.String())
	}
}

func TestJevErrorExitsTwo(t *testing.T) {
	project(t, billing)
	h := newHarness(&jevtest.Fake{Err: errors.New("401 invalid API key")})
	if code := h.run("retries failed payments"); code != exitError {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(h.errOut.String(), "invalid API key") {
		t.Fatalf("stderr %q", h.errOut.String())
	}
}

func TestBadFlagsExitTwoWithUsage(t *testing.T) {
	h := newHarness(scorer())
	if code := h.run("--threshold", "7", "q"); code != exitError || !strings.Contains(h.errOut.String(), "usage: jrep") {
		t.Fatalf("exit %d, stderr %q", code, h.errOut.String())
	}
	if code := h.run("-h"); code != exitMatch || !strings.Contains(h.out.String(), "usage: jrep") {
		t.Fatalf("help: exit %d", code)
	}
}

func TestJSONAndSeveralQuestions(t *testing.T) {
	project(t, billing)
	h := newHarness(scorer())
	code := h.run("--json", "retries failed payments", "-q", "sends email", "-q", "parses yaml")
	if code != exitMatch {
		t.Fatalf("exit %d, stderr %s", code, h.errOut.String())
	}
	want := `{"question":"retries failed payments","path":"src/billing/retry.go","start":1,"end":6,"probability":0.9,"signature":"func RetryFailedPayment(id string) error"}` + "\n" +
		`{"question":"sends email","path":"src/mail/send.go","start":1,"end":5,"probability":0.9,"signature":"func SendReceipt(to string) error"}` + "\n"
	if h.out.String() != want {
		t.Fatalf("got\n%s", h.out.String())
	}
	if want := "parses yaml: no matches above 0.60 (best: 0.10 src/billing/retry.go:1-6)\n"; h.errOut.String() != want {
		t.Fatalf("stderr %q", h.errOut.String())
	}
}

func TestOversizeFilesAreListedOnStderr(t *testing.T) {
	files := map[string]string{"dist/app.js": strings.Repeat("charge();\n", 200)}
	for k, v := range billing {
		files[k] = v
	}
	project(t, files)
	h := newHarness(scorer())
	h.run("--max-filesize", "1K", "retries failed payments")
	if want := "jrep: skipped 1 file over 1 KB: dist/app.js (use --max-filesize 0 to include)\n"; h.errOut.String() != want {
		t.Fatalf("stderr %q", h.errOut.String())
	}
	if strings.Contains(h.out.String(), "dist/app.js") {
		t.Fatalf("skipped file was searched: %s", h.out.String())
	}
}

func TestRepeatSearchIsServedFromCache(t *testing.T) {
	project(t, billing)
	fake := scorer()
	h := newHarness(fake)
	h.run("retries failed payments")
	first := len(fake.Requests())
	if code := h.run("--threshold", "0.05", "retries failed payments"); code != exitMatch {
		t.Fatalf("exit %d", code)
	}
	if len(fake.Requests()) != first {
		t.Fatalf("second search made %d new calls", len(fake.Requests())-first)
	}
	if n := strings.Count(h.out.String(), "\n"); n != 2 {
		t.Fatalf("lower threshold should show both files:\n%s", h.out.String())
	}
}
