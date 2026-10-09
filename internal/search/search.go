// Package search runs the pipeline: key → cache → Jev → rank.
package search

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"jrep/internal/cache"
	"jrep/internal/chunk"
	"jrep/internal/jev"
)

// DefaultWorkers is how many Jev calls may be in flight: enough for the
// client's 320 requests/s ceiling at ~250 ms a call. The client's rate
// limiter, not this number, keeps calls inside TypeSafe's limits.
const DefaultWorkers = 128

type Chunk struct {
	Path string // shown to the user
	Rel  string // sent to Jev and hashed; stable across working directories
	chunk.Chunk
}

type Options struct {
	Questions []string
	Model     string
	Threshold float64 // minimum probability to count as a match
	Top       int     // maximum matches per question; 0 means no limit
	ReadCache bool    // false still writes answers, but never reads them
	Workers   int     // concurrent Jev calls; 0 means DefaultWorkers

	// OnCacheError hears about failed cache writes, which don't stop a search.
	OnCacheError func(error)

	// OnAsk hears how many chunks are about to be sent to Jev, when any are.
	OnAsk func(chunks int)
}

type Match struct {
	Path        string
	Start, End  int
	Signature   string
	Probability float64
}

type Result struct {
	Question string
	Matches  []Match // best first, at or above the threshold
	Best     *Match  // best candidate even when below the threshold; nil if nothing was searched
}

// task is one chunk and the questions still unanswered for it.
type task struct {
	chunk   int
	state   jev.State
	missing []int // indexes into questions
}

// Run asks every question about every chunk, using cached answers where it
// can, and ranks the results. Each answer is cached as it arrives, so an
// interrupted run keeps its progress.
func Run(ctx context.Context, chunks []Chunk, client jev.Client, store cache.Cache, opts Options) ([]Result, error) {
	questions := dedupe(opts.Questions)
	instructions := make([]string, len(questions))
	for i, q := range questions {
		instructions[i] = jev.Instruction(q)
	}

	states, all, err := keyAll(chunks, opts.Model, instructions)
	if err != nil {
		return nil, err
	}
	// scores[c][q] is chunk c's probability for question q, and keys[c][q]
	// its cache key.
	scores := make([][]float64, len(chunks))
	keys := make([][]cache.Key, len(chunks))
	flat := make([]float64, len(all))
	for c := range chunks {
		scores[c] = flat[c*len(questions) : (c+1)*len(questions)]
		keys[c] = all[c*len(questions) : (c+1)*len(questions)]
	}

	hits := map[cache.Key]float64{}
	if opts.ReadCache && len(all) > 0 {
		var err error
		if hits, err = store.Get(all); err != nil {
			return nil, fmt.Errorf("reading cache: %w", err)
		}
	}
	var tasks []task
	for c := range chunks {
		t := task{chunk: c, state: states[c]}
		for q := range questions {
			if v, ok := hits[keys[c][q]]; ok {
				scores[c][q] = v
			} else {
				t.missing = append(t.missing, q)
			}
		}
		if len(t.missing) > 0 {
			tasks = append(tasks, t)
		}
	}

	ask := func(ctx context.Context, t task) error {
		req := jev.Request{Model: opts.Model, State: t.state}
		for _, q := range t.missing {
			req.Questions = append(req.Questions, instructions[q])
		}
		ch := chunks[t.chunk]
		resp, err := client.Ask(ctx, req)
		if err != nil {
			return fmt.Errorf("%s:%d-%d: %w", ch.Path, ch.Start, ch.End, err)
		}
		if len(resp.Nouls) != len(t.missing) {
			return fmt.Errorf("%s:%d-%d: got %d answers for %d questions", ch.Path, ch.Start, ch.End, len(resp.Nouls), len(t.missing))
		}
		for i, q := range t.missing {
			scores[t.chunk][q] = resp.Nouls[i]
			if err := store.Put(keys[t.chunk][q], resp.Nouls[i]); err != nil && opts.OnCacheError != nil {
				opts.OnCacheError(err)
			}
		}
		return nil
	}
	if len(tasks) > 0 && opts.OnAsk != nil {
		opts.OnAsk(len(tasks))
	}
	if err := parallel(ctx, tasks, cmp.Or(opts.Workers, DefaultWorkers), ask); err != nil {
		return nil, err
	}

	results := make([]Result, len(questions))
	for q, question := range questions {
		results[q] = rank(question, chunks, scores, q, opts)
	}
	return results, nil
}

// stateOf is what Jev is shown about a chunk, and its encoding for cache
// keys.
func stateOf(ch Chunk) (jev.State, []byte, error) {
	s := jev.State{Path: ch.Rel, Code: ch.Code}
	b, err := json.Marshal(s)
	return s, b, err
}

// keyAll builds every chunk's state and its cache key for each
// instruction, keys[c*len(instructions)+q], one worker per CPU: hashing
// every chunk once per question is most of a cached search's time.
func keyAll(chunks []Chunk, model string, instructions []string) ([]jev.State, []cache.Key, error) {
	states := make([]jev.State, len(chunks))
	keys := make([]cache.Key, len(chunks)*len(instructions))
	errs := make([]error, len(chunks))
	each(len(chunks), func(c int) {
		var sent []byte
		if states[c], sent, errs[c] = stateOf(chunks[c]); errs[c] != nil {
			return
		}
		for q, in := range instructions {
			keys[c*len(instructions)+q] = cache.KeyOf(model, in, sent)
		}
	})
	return states, keys, errors.Join(errs...)
}

// ErrNotAsked means no chunk has a cached answer for the question.
var ErrNotAsked = errors.New("no cached answers")

// Narrow keeps the n chunks that scored best for an earlier question, in
// their original order, reading only cached answers. It makes retrying a
// reworded question cheap: replaying 24 rewordings of 12 tasks, the right
// chunk was always in the earlier question's top 11, and asking the new
// wording about only those ranked it as well as a full scan did. Chunks
// with no cached answer are left out: the earlier search never saw them.
func Narrow(chunks []Chunk, store cache.Cache, model, question string, n int) ([]Chunk, error) {
	_, keys, err := keyAll(chunks, model, []string{jev.Instruction(question)})
	if err != nil {
		return nil, err
	}
	hits, err := store.Get(keys)
	if err != nil {
		return nil, fmt.Errorf("reading cache: %w", err)
	}
	var answered []int
	for i, k := range keys {
		if _, ok := hits[k]; ok {
			answered = append(answered, i)
		}
	}
	if len(answered) == 0 {
		return nil, ErrNotAsked
	}
	slices.SortStableFunc(answered, func(a, b int) int { return cmp.Compare(hits[keys[b]], hits[keys[a]]) })
	answered = answered[:min(n, len(answered))]
	slices.Sort(answered)
	kept := make([]Chunk, len(answered))
	for j, i := range answered {
		kept[j] = chunks[i]
	}
	return kept, nil
}

// parallel runs fn over tasks with n workers and stops at the first error.
func parallel(ctx context.Context, tasks []task, n int, fn func(context.Context, task) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	work := make(chan task)
	var wg sync.WaitGroup
	for range min(n, len(tasks)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range work {
				if err := fn(ctx, t); err != nil {
					cancel(err)
				}
			}
		}()
	}
feed:
	for _, t := range tasks {
		select {
		case work <- t:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	return context.Cause(ctx)
}

// rank sorts only the chunks at or above the threshold, usually a handful,
// and finds the best of the rest in one pass.
func rank(question string, chunks []Chunk, scores [][]float64, q int, opts Options) Result {
	match := func(c int) Match {
		ch := chunks[c]
		return Match{Path: ch.Path, Start: ch.Start, End: ch.End, Signature: ch.Signature, Probability: scores[c][q]}
	}
	better := func(a, b Match) int {
		return cmp.Or(
			cmp.Compare(b.Probability, a.Probability),
			cmp.Compare(a.Path, b.Path),
			cmp.Compare(a.Start, b.Start),
		)
	}
	res := Result{Question: question}
	best := -1
	for c := range chunks {
		if best < 0 || better(match(c), match(best)) < 0 {
			best = c
		}
		if scores[c][q] >= opts.Threshold {
			res.Matches = append(res.Matches, match(c))
		}
	}
	if best >= 0 {
		m := match(best)
		res.Best = &m
	}
	slices.SortStableFunc(res.Matches, better)
	if opts.Top > 0 && len(res.Matches) > opts.Top {
		res.Matches = res.Matches[:opts.Top]
	}
	return res
}

// dedupe drops questions that differ only in case or white space.
func dedupe(questions []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, q := range questions {
		q = jev.NormalizeSpace(q)
		if k := strings.ToLower(q); !seen[k] {
			seen[k] = true
			out = append(out, q)
		}
	}
	return out
}
