// Package search runs the pipeline: key → cache → Jev → rank.
package search

import (
	"cmp"
	"context"
	"encoding/json"
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

	// scores[c][q] is chunk c's probability for question q.
	scores := make([][]float64, len(chunks))
	keys := make([][]cache.Key, len(chunks))
	states := make([]jev.State, len(chunks))
	var all []cache.Key
	for c, ch := range chunks {
		states[c] = jev.State{Path: ch.Rel, Lines: fmt.Sprintf("%d-%d", ch.Start, ch.End), Code: ch.Code}
		sent, err := json.Marshal(states[c])
		if err != nil {
			return nil, err
		}
		scores[c] = make([]float64, len(questions))
		keys[c] = make([]cache.Key, len(questions))
		for q := range questions {
			keys[c][q] = cache.KeyOf(opts.Model, instructions[q], sent)
			all = append(all, keys[c][q])
		}
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
		resp, err := client.Ask(ctx, req)
		if err != nil {
			return fmt.Errorf("%s:%s: %w", chunks[t.chunk].Path, t.state.Lines, err)
		}
		if len(resp.Nouls) != len(t.missing) {
			return fmt.Errorf("%s:%s: got %d answers for %d questions", chunks[t.chunk].Path, t.state.Lines, len(resp.Nouls), len(t.missing))
		}
		for i, q := range t.missing {
			scores[t.chunk][q] = resp.Nouls[i]
			if err := store.Put(keys[t.chunk][q], resp.Nouls[i]); err != nil && opts.OnCacheError != nil {
				opts.OnCacheError(err)
			}
		}
		return nil
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

func rank(question string, chunks []Chunk, scores [][]float64, q int, opts Options) Result {
	all := make([]Match, len(chunks))
	for c, ch := range chunks {
		all[c] = Match{Path: ch.Path, Start: ch.Start, End: ch.End, Signature: ch.Signature, Probability: scores[c][q]}
	}
	slices.SortStableFunc(all, func(a, b Match) int {
		return cmp.Or(
			cmp.Compare(b.Probability, a.Probability),
			cmp.Compare(a.Path, b.Path),
			cmp.Compare(a.Start, b.Start),
		)
	})
	res := Result{Question: question}
	if len(all) > 0 {
		best := all[0]
		res.Best = &best
	}
	for _, m := range all {
		if m.Probability < opts.Threshold || (opts.Top > 0 && len(res.Matches) == opts.Top) {
			break
		}
		res.Matches = append(res.Matches, m)
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
