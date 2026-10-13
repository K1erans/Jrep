// Package describe has Claude write what each chunk of code does, in plain
// words, so jrep reuse can ask Jev about the description instead of the
// code, in the same call a search makes. On jrep's own code, Jev set the
// chunk being looked for apart from the rest far more clearly that way:
// "skips lockfiles" scored 0.91 against 0.47 for the next chunk, where the
// code scored 0.89 against 0.94. Client is the seam: Anthropic and
// ClaudeCode call Claude, describetest.Fake answers offline.
package describe

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"jrep/internal/cache"
	"jrep/internal/jev"
)

// Chunk is a piece of code as Claude is shown it.
type Chunk struct {
	Path string // repo-relative, so descriptions are shared across working directories
	Code string
}

// Plan is what an agent is about to write: a feature in words, or draft
// code when Feature is empty.
type Plan struct {
	Feature string
	Draft   Chunk
}

// Questions are what jrep reuse asks Jev about every description.
type Questions struct {
	Does  string `json:"does"`  // what the new code does
	Broad string `json:"broad"` // the broader capability that code worth extending would have
}

type Client interface {
	// Model names the model writing descriptions. It is part of every
	// cache key.
	Model() string

	// Describe returns one description per chunk, in order.
	Describe(ctx context.Context, chunks []Chunk) ([]string, error)

	// Phrase turns a plan into questions. Asked about "writes matches as
	// CSV", Haiku broadened it to "exports search results to an output
	// format", which found the JSON writer; asked "could code be extended
	// to write CSV", Jev favoured config structs and main, since almost
	// any code could be.
	Phrase(ctx context.Context, plan Plan) (Questions, error)
}

const (
	// batchChunks and batchBytes cap one Describe call. Haiku described
	// 15 of jrep's chunks in about 10 s, 3,400 tokens of code in.
	batchChunks = 15
	batchBytes  = 64 << 10 // a bigger chunk goes alone

	// DefaultWorkers is how many Claude calls may be in flight. The SDK
	// retries rate limits, so more mostly buys 429s.
	DefaultWorkers = 8
)

type Options struct {
	ReadCache bool // false still saves descriptions, but never reads them
	Workers   int  // concurrent Claude calls; 0 means DefaultWorkers

	// OnCacheError hears about failed cache reads and writes, which don't
	// stop anything.
	OnCacheError func(error)

	// OnDescribe hears how many chunks are about to be sent to Claude,
	// when any are.
	OnDescribe func(chunks int)
}

func (o Options) cacheError(err error) {
	if o.OnCacheError != nil {
		o.OnCacheError(err)
	}
}

// Describe returns a description of every chunk, from the cache where it
// can, asking client about the rest in batches. Each batch is cached as it
// arrives, so an interrupted run keeps its progress. A description is keyed
// by the model, the prompt and the chunk, so editing one function describes
// only that function again.
func Describe(ctx context.Context, chunks []Chunk, client Client, store cache.Cache, opts Options) ([]string, error) {
	keys := make([]cache.Key, len(chunks))
	for i, c := range chunks {
		keys[i] = cache.Hash([]byte("describe"), []byte(client.Model()), []byte(describePrompt), []byte(c.Path), []byte(c.Code))
	}
	out := make([]string, len(chunks))
	var missing []int
	hits := map[cache.Key]string{}
	if opts.ReadCache {
		var err error
		if hits, err = store.GetDescriptions(keys); err != nil {
			opts.cacheError(fmt.Errorf("reading description cache: %w", err))
		}
	}
	for i, k := range keys {
		if d, ok := hits[k]; ok {
			out[i] = d
		} else {
			missing = append(missing, i)
		}
	}
	if len(missing) == 0 {
		return out, nil
	}
	if opts.OnDescribe != nil {
		opts.OnDescribe(len(missing))
	}

	describe := func(ctx context.Context, batch []int) error {
		cs := make([]Chunk, len(batch))
		for j, i := range batch {
			cs[j] = chunks[i]
		}
		ds, err := client.Describe(ctx, cs)
		if err != nil {
			return err
		}
		if len(ds) != len(batch) {
			return fmt.Errorf("got %d descriptions for %d chunks", len(ds), len(batch))
		}
		save := make(map[cache.Key]string, len(batch))
		for j, i := range batch {
			out[i] = ds[j]
			save[keys[i]] = ds[j]
		}
		if err := store.PutDescriptions(save); err != nil {
			opts.cacheError(fmt.Errorf("saving descriptions: %w", err))
		}
		return nil
	}
	if err := parallel(ctx, batches(chunks, missing), cmp.Or(opts.Workers, DefaultWorkers), describe); err != nil {
		return nil, err
	}
	return out, nil
}

// batches groups chunk indexes into Claude calls, in order, up to
// batchChunks chunks and batchBytes of code each.
func batches(chunks []Chunk, idx []int) [][]int {
	var out [][]int
	size := 0
	for _, i := range idx {
		n := len(chunks[i].Code)
		if k := len(out); k > 0 && len(out[k-1]) < batchChunks && size+n <= batchBytes {
			out[k-1] = append(out[k-1], i)
			size += n
			continue
		}
		out = append(out, []int{i})
		size = n
	}
	return out
}

// Phrase turns a plan into questions, cached like descriptions so a
// repeat reuse check needs no Claude call. Given a feature in words, Does
// is the feature itself: they are the agent's own words.
func Phrase(ctx context.Context, plan Plan, client Client, store cache.Cache, opts Options) (Questions, error) {
	plan.Feature = jev.NormalizeSpace(plan.Feature)
	key := cache.Hash([]byte("phrase"), []byte(client.Model()), []byte(phrasePrompt),
		[]byte(plan.Feature), []byte(plan.Draft.Path), []byte(plan.Draft.Code))
	var q Questions
	cached := false
	if opts.ReadCache {
		hits, err := store.GetDescriptions([]cache.Key{key})
		if err != nil {
			opts.cacheError(fmt.Errorf("reading description cache: %w", err))
		}
		cached = hits[key] != "" && json.Unmarshal([]byte(hits[key]), &q) == nil
	}
	if !cached {
		var err error
		if q, err = client.Phrase(ctx, plan); err != nil {
			return Questions{}, err
		}
		b, _ := json.Marshal(q) // can't fail: two strings
		if err := store.PutDescriptions(map[cache.Key]string{key: string(b)}); err != nil {
			opts.cacheError(fmt.Errorf("saving descriptions: %w", err))
		}
	}
	if plan.Feature != "" {
		q.Does = plan.Feature
	}
	return q, nil
}

// parallel runs fn over items with n workers and stops at the first error.
func parallel[T any](ctx context.Context, items []T, n int, fn func(context.Context, T) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	work := make(chan T)
	var wg sync.WaitGroup
	for range min(n, len(items)) {
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
	for _, t := range items {
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
