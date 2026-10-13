// Package describetest provides a deterministic offline describer for tests.
package describetest

import (
	"cmp"
	"context"
	"sync"

	"jrep/internal/describe"
)

// Fake describes each chunk with Summary, by default its code unchanged,
// and phrases each plan with Phrases, by default the feature as both
// questions.
type Fake struct {
	Name    string // the model, "fake" when empty
	Summary func(describe.Chunk) string
	Phrases func(describe.Plan) describe.Questions
	Err     error // returned by every call when set

	mu      sync.Mutex
	batches [][]describe.Chunk
	plans   []describe.Plan
}

func (f *Fake) Model() string { return cmp.Or(f.Name, "fake") }

func (f *Fake) Describe(ctx context.Context, chunks []describe.Chunk) ([]string, error) {
	f.mu.Lock()
	f.batches = append(f.batches, chunks)
	f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.Code
		if f.Summary != nil {
			out[i] = f.Summary(c)
		}
	}
	return out, nil
}

func (f *Fake) Phrase(ctx context.Context, plan describe.Plan) (describe.Questions, error) {
	f.mu.Lock()
	f.plans = append(f.plans, plan)
	f.mu.Unlock()
	if f.Err != nil {
		return describe.Questions{}, f.Err
	}
	if f.Phrases != nil {
		return f.Phrases(plan), nil
	}
	return describe.Questions{Does: plan.Feature, Broad: plan.Feature}, nil
}

// Batches returns the chunks of every Describe call so far.
func (f *Fake) Batches() [][]describe.Chunk {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]describe.Chunk(nil), f.batches...)
}

// Plans returns every plan phrased so far.
func (f *Fake) Plans() []describe.Plan {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]describe.Plan(nil), f.plans...)
}
