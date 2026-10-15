package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/K1erans/jinter/cache"
	"github.com/K1erans/jinter/jev"
	"github.com/K1erans/jinter/jev/jevtest"
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
		client: func() (jev.Client, error) { return client, nil },
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
	if want := "jrep: asking Jev about 2 chunks\nparses yaml: no matches above 0.60 (best: 0.10 src/billing/retry.go:1-6)\n"; h.errOut.String() != want {
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
	if want := "jrep: asking Jev about 2 chunks\njrep: skipped 1 file over 1 KB: dist/app.js (use --max-filesize 0 to include)\n"; h.errOut.String() != want {
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

func TestClearCacheForgetsAnswers(t *testing.T) {
	project(t, billing)
	t.Setenv("JREP_CACHE_DIR", t.TempDir())
	fake := scorer()
	h := newHarness(fake)
	disk := realApp(&h.out, &h.errOut)
	h.openCache, h.clearCache = disk.openCache, disk.clearCache

	h.run("retries failed payments")
	calls := len(fake.Requests())
	h.run("retries failed payments")
	if len(fake.Requests()) != calls {
		t.Fatal("second search should come from the SQLite cache")
	}
	if code := h.run("--clear-cache"); code != exitMatch {
		t.Fatalf("clear: exit %d, %s", code, h.errOut.String())
	}
	h.run("retries failed payments")
	if len(fake.Requests()) != 2*calls {
		t.Fatal("after --clear-cache every chunk is asked again")
	}
}

func TestUnusableCacheWarnsButSearches(t *testing.T) {
	project(t, billing)
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, nil, 0o644)
	t.Setenv("JREP_CACHE_DIR", filepath.Join(blocker, "sub")) // can't create a folder under a file
	h := newHarness(scorer())
	h.openCache = realApp(&h.out, &h.errOut).openCache
	if code := h.run("retries failed payments"); code != exitMatch {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(h.errOut.String(), "cache unavailable") {
		t.Fatalf("stderr %q", h.errOut.String())
	}
}

func TestPrepassSendsOnlyChunksWithKeywords(t *testing.T) {
	project(t, billing)
	fake := scorer()
	h := newHarness(fake)
	if code := h.run("-s", "retries failed payments"); code != exitMatch {
		t.Fatalf("exit %d, %s", code, h.errOut.String())
	}
	reqs := fake.Requests()
	if len(reqs) != 1 || reqs[0].Path != "src/billing/retry.go" {
		t.Fatalf("only the chunk mentioning the keywords should be sent, got %d requests", len(reqs))
	}

	fake2 := scorer()
	h = newHarness(fake2)
	h.run("retries failed payments")
	if len(fake2.Requests()) != 2 {
		t.Fatal("without -s every chunk goes to Jev")
	}
}

func TestRefineAsksOnlyAboutChunksThatMatchedTheEarlierQuestion(t *testing.T) {
	project(t, billing)
	fake := scorer()
	h := newHarness(fake)
	h.run("-s", "retries failed payments") // answers only for retry.go
	if code := h.run("--refine", "retries failed payments", "sends email"); code != exitNoMatch {
		t.Fatalf("exit %d, stderr %s", code, h.errOut.String())
	}
	reqs := fake.Requests()[1:]
	if len(reqs) != 1 || reqs[0].Path != "src/billing/retry.go" {
		t.Fatalf("only chunks answered for the earlier question should be sent, got %d requests", len(reqs))
	}
	if want := "jrep: asking Jev about 1 chunk\njrep: --refine: searched the 1 chunk that best matched \"retries failed payments\"\n"; h.errOut.String() != want {
		t.Fatalf("stderr %q", h.errOut.String())
	}

	if code := h.run("--refine", "parses yaml", "sends email"); code != exitError || !strings.Contains(h.errOut.String(), `no cached answers for "parses yaml"`) {
		t.Fatalf("refining by a question never asked: exit %d, stderr %q", code, h.errOut.String())
	}
	if len(fake.Requests()) != 2 {
		t.Fatal("a failed --refine sends nothing")
	}
}

func TestPrepassThatKeepsNothingSaysWhy(t *testing.T) {
	project(t, billing)
	h := newHarness(scorer())
	if code := h.run("-s", "--keywords", "kafka|rabbitmq", "retries failed payments"); code != exitNoMatch {
		t.Fatalf("exit %d", code)
	}
	if want := "jrep: -s kept no chunks (keywords: kafka|rabbitmq)\n"; h.errOut.String() != want {
		t.Fatalf("stderr %q", h.errOut.String())
	}
}

func TestNoAPIKeyOnlyFailsWhenJevIsNeeded(t *testing.T) {
	project(t, billing)
	t.Setenv("JREP_CACHE_DIR", t.TempDir())
	t.Setenv("TYPESAFE_API_KEY", "")
	h := newHarness(scorer())
	disk := realApp(&h.out, &h.errOut)
	h.openCache = disk.openCache

	h.run("retries failed payments") // fills the cache through the fake
	h.client = disk.client
	if code := h.run("retries failed payments"); code != exitMatch {
		t.Fatalf("a fully cached search needs no key; exit %d, %s", code, h.errOut.String())
	}
	if code := h.run("sends email"); code != exitError || !strings.Contains(h.errOut.String(), "TYPESAFE_API_KEY is not set") {
		t.Fatalf("exit %d, stderr %q", code, h.errOut.String())
	}
}

func TestBadRateOverrideIsAnError(t *testing.T) {
	project(t, billing)
	t.Setenv("TYPESAFE_API_KEY", "k")
	t.Setenv("JREP_MAX_RPS", "fast")
	h := newHarness(scorer())
	h.client = realApp(&h.out, &h.errOut).client
	if code := h.run("q"); code != exitError || !strings.Contains(h.errOut.String(), "JREP_MAX_RPS") {
		t.Fatalf("exit %d, stderr %q", code, h.errOut.String())
	}
}

// TestRealWiringAgainstAFakeServer runs the production client and SQLite
// cache against a local stand-in for TypeSafe.
func TestRealWiringAgainstAFakeServer(t *testing.T) {
	project(t, billing)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			State struct{ Code string } `json:"state"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		noul := 0.1
		if strings.Contains(body.State.Code, "charge") {
			noul = 0.9
		}
		fmt.Fprintf(w, `{"model":"jev-1.13.0","answers":{"q0":{"type":"noul","noul":%v}}}`, noul)
	}))
	defer srv.Close()
	t.Setenv("TYPESAFE_API_KEY", "k")
	t.Setenv("TYPESAFE_BASE_URL", srv.URL)
	t.Setenv("JREP_CACHE_DIR", t.TempDir())

	var out, errOut bytes.Buffer
	a := realApp(&out, &errOut)
	if code := a.run(context.Background(), []string{"retries failed payments"}); code != exitMatch {
		t.Fatalf("exit %d, %s", code, errOut.String())
	}
	if want := "0.90  src/billing/retry.go:1-6  func RetryFailedPayment(id string) error\n"; out.String() != want {
		t.Fatalf("got %q", out.String())
	}
	if code := a.run(context.Background(), []string{"retries failed payments"}); code != exitMatch || calls.Load() != 2 {
		t.Fatalf("second run: exit %d, %d calls in total", code, calls.Load())
	}
}

func TestTestFilesAreLeftOutAndTheNoMatchHintSaysSo(t *testing.T) {
	files := map[string]string{"src/billing/retry_test.go": "package billing\n\nfunc TestCharge() { chargeYaml() }\n"}
	for k, v := range billing {
		files[k] = v
	}
	project(t, files)
	fake := scorer()
	h := newHarness(fake)
	if code := h.run("parses yaml"); code != exitNoMatch {
		t.Fatalf("exit %d", code)
	}
	for _, r := range fake.Requests() {
		if strings.HasSuffix(r.Path, "_test.go") {
			t.Fatal("test files aren't sent by default")
		}
	}
	if want := "jrep: asking Jev about 2 chunks\njrep: 1 test file not searched (use --tests to include)\n"; h.errOut.String() != want {
		t.Fatalf("stderr %q", h.errOut.String())
	}

	if code := h.run("--tests", "parses yaml"); code != exitMatch {
		t.Fatalf("--tests: exit %d, %s", code, h.errOut.String())
	}
	if !strings.Contains(h.out.String(), "src/billing/retry_test.go:1-3") || h.errOut.String() != "jrep: asking Jev about 1 chunk\n" {
		t.Fatalf("out %q err %q", h.out.String(), h.errOut.String())
	}

	if code := h.run("retries failed payments"); code != exitMatch {
		t.Fatalf("exit %d", code)
	}
	if want := "jrep: asking Jev about 2 chunks\njrep: 1 test file not searched (use --tests to include)\n"; h.errOut.String() != want {
		t.Fatalf("skipped tests are reported even when something matched; stderr %q", h.errOut.String())
	}
}

func TestAQuestionAboutTestsSearchesTestFiles(t *testing.T) {
	files := map[string]string{"src/billing/retry_test.go": "package billing\n\nfunc TestCharge() { chargeYaml() }\n"}
	for k, v := range billing {
		files[k] = v
	}
	project(t, files)
	fake := &jevtest.Fake{Score: jevtest.Contains(map[string]string{"the test that parses yaml": "yaml"})}
	h := newHarness(fake)
	if code := h.run("the test that parses yaml"); code != exitMatch {
		t.Fatalf("exit %d, %s", code, h.errOut.String())
	}
	if !strings.Contains(h.out.String(), "src/billing/retry_test.go:1-3") {
		t.Fatalf("out %q", h.out.String())
	}
	if want := "jrep: asking Jev about 3 chunks\njrep: searched test files too, since the question is about tests\n"; h.errOut.String() != want {
		t.Fatalf("stderr %q", h.errOut.String())
	}
}

func TestTranslationsAreLeftOutAndReported(t *testing.T) {
	files := map[string]string{"locales/de.json": "{\"charge\": \"belasten\"}\n", "po/fr.po": "msgid \"charge\"\n"}
	for k, v := range billing {
		files[k] = v
	}
	project(t, files)
	fake := scorer()
	h := newHarness(fake)
	if code := h.run("retries failed payments"); code != exitMatch {
		t.Fatalf("exit %d, %s", code, h.errOut.String())
	}
	if n := len(fake.Requests()); n != 2 {
		t.Fatalf("only the two source files should be sent, got %d requests", n)
	}
	if want := "jrep: 2 translation files not searched (name them to include)\n"; !strings.HasSuffix(h.errOut.String(), want) {
		t.Fatalf("stderr %q", h.errOut.String())
	}
}

func TestStderrSaysHowManyChunksJevIsAskedAbout(t *testing.T) {
	project(t, billing)
	h := newHarness(scorer())
	h.run("retries failed payments")
	if want := "jrep: asking Jev about 2 chunks\n"; h.errOut.String() != want {
		t.Fatalf("stderr %q", h.errOut.String())
	}
	h.run("retries failed payments")
	if h.errOut.String() != "" {
		t.Fatalf("a fully cached search asks nothing and says nothing: %q", h.errOut.String())
	}
}

func TestDocsAndGeneratedFilesAreLeftOutAndReported(t *testing.T) {
	files := map[string]string{
		"README.md":         "# Billing\n\nWe charge cards and retry.\n",
		"package-lock.json": "{\"charge\": 1}\n",
	}
	for k, v := range billing {
		files[k] = v
	}
	project(t, files)
	fake := scorer()
	h := newHarness(fake)
	if code := h.run("retries failed payments"); code != exitMatch {
		t.Fatalf("exit %d, %s", code, h.errOut.String())
	}
	for _, r := range fake.Requests() {
		if r.Path == "README.md" || r.Path == "package-lock.json" {
			t.Fatalf("%s isn't sent by default", r.Path)
		}
	}
	want := "jrep: asking Jev about 2 chunks\n" +
		"jrep: 1 generated file not searched (lockfiles, source maps, minified, SVG, node_modules; name it to include)\n" +
		"jrep: 1 doc not searched (use --docs to include)\n"
	if h.errOut.String() != want {
		t.Fatalf("stderr %q", h.errOut.String())
	}

	if code := h.run("--docs", "retries failed payments"); code != exitMatch {
		t.Fatalf("--docs: exit %d", code)
	}
	if !strings.Contains(h.out.String(), "README.md:1-3") {
		t.Fatalf("--docs should search README.md: %q", h.out.String())
	}
}

func TestWorktreesShareTheCache(t *testing.T) {
	project(t, billing)
	mainDir, _ := os.Getwd()
	wt := filepath.Join(t.TempDir(), "wt")
	for _, args := range [][]string{
		{"init", "-q"}, {"add", "."},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init"},
		{"worktree", "add", "-q", wt},
	} {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Setenv("JREP_CACHE_DIR", t.TempDir())
	fake := scorer()
	h := newHarness(fake)
	disk := realApp(&h.out, &h.errOut)
	h.openCache, h.clearCache = disk.openCache, disk.clearCache

	h.run("retries failed payments")
	calls := len(fake.Requests())
	t.Chdir(wt)
	if code := h.run("retries failed payments"); code != exitMatch {
		t.Fatalf("exit %d, %s", code, h.errOut.String())
	}
	if len(fake.Requests()) != calls {
		t.Fatalf("a worktree should reuse the main checkout's answers, made %d new calls", len(fake.Requests())-calls)
	}

	h.run("--clear-cache")
	t.Chdir(mainDir)
	h.run("retries failed payments")
	if len(fake.Requests()) != 2*calls {
		t.Fatal("--clear-cache in a worktree clears the shared cache")
	}
}
