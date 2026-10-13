package describe_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"jrep/internal/cache"
	"jrep/internal/describe"
	"jrep/internal/describe/describetest"
)

func chunks(n int) []describe.Chunk {
	cs := make([]describe.Chunk, n)
	for i := range cs {
		cs[i] = describe.Chunk{Path: fmt.Sprintf("src/f%d.go", i), Code: fmt.Sprintf("func f%d() {}", i)}
	}
	return cs
}

var opts = describe.Options{ReadCache: true}

func batchSizes(f *describetest.Fake) []int {
	var sizes []int
	for _, b := range f.Batches() {
		sizes = append(sizes, len(b))
	}
	slices.Sort(sizes)
	return sizes
}

func TestDescribesInBatchesAndReadsThemBackFromTheCache(t *testing.T) {
	f := &describetest.Fake{Summary: func(c describe.Chunk) string { return "does " + c.Path }}
	store := cache.NewMemory()
	cs := chunks(20)
	asked := 0
	o := opts
	o.OnDescribe = func(n int) { asked += n }

	got, err := describe.Describe(context.Background(), cs, f, store, o)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "does src/f0.go" || got[19] != "does src/f19.go" {
		t.Fatalf("descriptions out of order: %q", got)
	}
	if sizes := batchSizes(f); !slices.Equal(sizes, []int{5, 15}) || asked != 20 {
		t.Fatalf("batches %v, told about %d", sizes, asked)
	}

	again, err := describe.Describe(context.Background(), cs, f, store, o)
	if err != nil || !slices.Equal(again, got) || len(f.Batches()) != 2 || asked != 20 {
		t.Fatalf("a repeat should come from the cache: err %v, %d calls", err, len(f.Batches()))
	}
}

func TestOnlyEditedChunksAreDescribedAgain(t *testing.T) {
	f := &describetest.Fake{}
	store := cache.NewMemory()
	cs := chunks(3)
	if _, err := describe.Describe(context.Background(), cs, f, store, opts); err != nil {
		t.Fatal(err)
	}
	cs[1].Code = "func f1() { edited() }"
	got, err := describe.Describe(context.Background(), cs, f, store, opts)
	if err != nil {
		t.Fatal(err)
	}
	b := f.Batches()
	if len(b) != 2 || len(b[1]) != 1 || b[1][0].Code != cs[1].Code || got[1] != cs[1].Code {
		t.Fatalf("calls %v, got %q", b, got)
	}
}

func TestADescriptionDependsOnTheModel(t *testing.T) {
	store := cache.NewMemory()
	describe.Describe(context.Background(), chunks(1), &describetest.Fake{Name: "haiku"}, store, opts)
	f := &describetest.Fake{Name: "sonnet"}
	describe.Describe(context.Background(), chunks(1), f, store, opts)
	if len(f.Batches()) != 1 {
		t.Fatalf("another model should describe again, got %d calls", len(f.Batches()))
	}
}

func TestBigChunksGoInBatchesOfTheirOwn(t *testing.T) {
	f := &describetest.Fake{}
	cs := chunks(4)
	cs[1].Code = strings.Repeat("x", 40<<10)
	cs[2].Code = strings.Repeat("y", 40<<10)
	if _, err := describe.Describe(context.Background(), cs, f, cache.NewMemory(), opts); err != nil {
		t.Fatal(err)
	}
	if sizes := batchSizes(f); !slices.Equal(sizes, []int{2, 2}) {
		t.Fatalf("batches %v: 64 KB of code at most per call", sizes)
	}
}

func TestADescribeErrorStopsButKeepsEarlierBatches(t *testing.T) {
	f := &describetest.Fake{Err: errors.New("no Anthropic credentials found")}
	_, err := describe.Describe(context.Background(), chunks(2), f, cache.NewMemory(), opts)
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("err %v", err)
	}
}

func TestWithoutReadingTheCacheDescriptionsAreStillSaved(t *testing.T) {
	f := &describetest.Fake{}
	store := cache.NewMemory()
	o := opts
	o.ReadCache = false
	describe.Describe(context.Background(), chunks(1), f, store, o)
	describe.Describe(context.Background(), chunks(1), f, store, o)
	describe.Describe(context.Background(), chunks(1), f, store, opts)
	if len(f.Batches()) != 2 {
		t.Fatalf("want 2 calls, then a cache hit; got %d calls", len(f.Batches()))
	}
}

func TestPhraseKeepsTheAgentsWordsAndIsCached(t *testing.T) {
	f := &describetest.Fake{Phrases: func(describe.Plan) describe.Questions {
		return describe.Questions{Does: "writes csv", Broad: "exports results to an output format"}
	}}
	store := cache.NewMemory()
	plan := describe.Plan{Feature: "writes  matches as\nCSV"}
	q, err := describe.Phrase(context.Background(), plan, f, store, opts)
	if err != nil {
		t.Fatal(err)
	}
	want := describe.Questions{Does: "writes matches as CSV", Broad: "exports results to an output format"}
	if q != want {
		t.Fatalf("got %+v", q)
	}
	if again, err := describe.Phrase(context.Background(), plan, f, store, opts); err != nil || again != want || len(f.Plans()) != 1 {
		t.Fatalf("a repeat should come from the cache: %+v, %v, %d calls", again, err, len(f.Plans()))
	}

	draft := describe.Plan{Draft: describe.Chunk{Path: "draft.go", Code: "func writeCSV() {}"}}
	if q, _ := describe.Phrase(context.Background(), draft, f, store, opts); q.Does != "writes csv" {
		t.Fatalf("a draft's Does comes from Claude, got %+v", q)
	}
}
