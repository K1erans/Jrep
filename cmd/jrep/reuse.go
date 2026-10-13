package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"jrep/internal/describe"
	"jrep/internal/output"
	"jrep/internal/search"
	"jrep/internal/walk"
)

func (a app) runReuse(ctx context.Context, args []string) int {
	cfg, err := parseReuseArgs(args)
	if errors.Is(err, errHelp) {
		fmt.Fprint(a.stdout, usage)
		return exitMatch
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "jrep reuse: %v\n\n%s", err, usage)
		return exitError
	}
	code, err := a.reuse(ctx, cfg)
	if err != nil {
		fmt.Fprintf(a.stderr, "jrep reuse: %v\n", err)
		return exitError
	}
	return code
}

// reuse looks for code to build on before an agent writes new code. Claude
// describes every chunk and turns the plan into two questions: what the new
// code does, and the broader kind of work it is part of. Jev answers both
// about each description. Code that does the thing itself wins over code
// that does the broader work, as the closer evidence; with neither above
// the threshold, the answer is to create.
func (a app) reuse(ctx context.Context, cfg reuseConfig) (int, error) {
	plan := describe.Plan{Feature: cfg.Feature}
	if cfg.Draft != "" {
		draft, err := a.readDraft(cfg.Draft)
		if err != nil {
			return exitError, err
		}
		plan.Draft = draft
	}

	tests := cfg.Tests || aboutTests([]string{cfg.Feature})
	if tests && !cfg.Tests {
		fmt.Fprintln(a.stderr, "jrep: searching test files too, since the question is about tests")
	}
	w, err := a.load(cfg.Paths, walk.Options{NoIgnore: cfg.NoIgnore, Tests: tests, Docs: cfg.Docs, MaxFileSize: cfg.MaxFileSize}, cfg.Model, cfg.NoCache)
	if err != nil {
		return exitError, err
	}
	defer w.close()

	claude, err := a.describer()
	if err != nil {
		return exitError, err
	}
	opts := describe.Options{
		ReadCache:    !cfg.NoCache,
		OnCacheError: w.cacheError,
		OnDescribe: func(n int) {
			fmt.Fprintf(a.stderr, "jrep: describing %d %s with %s\n", n, plural(n, "chunk", "chunks"), claude.Model())
		},
	}
	q, err := describe.Phrase(ctx, plan, claude, w.store, opts)
	if err != nil {
		return exitError, fmt.Errorf("asking Claude to phrase the question: %w", err)
	}
	if plan.Feature == "" {
		fmt.Fprintf(a.stderr, "jrep: the draft %s\n", q.Does)
	}
	fmt.Fprintf(a.stderr, "jrep: also asking for code that %s\n", q.Broad)

	code := make([]describe.Chunk, len(w.chunks))
	for i, c := range w.chunks {
		code[i] = describe.Chunk{Path: c.Rel, Code: c.Code}
	}
	descriptions, err := describe.Describe(ctx, code, claude, w.store, opts)
	if err != nil {
		return exitError, fmt.Errorf("asking Claude to describe the code: %w", err)
	}
	summaries := make([]search.Chunk, len(w.chunks))
	for i, c := range w.chunks {
		summaries[i] = c
		summaries[i].Code = descriptions[i]
	}

	client, err := a.client()
	if err != nil {
		return exitError, err
	}
	results, err := search.Run(ctx, summaries, client, w.store, search.Options{
		Questions:    []string{q.Does, q.Broad},
		Model:        cfg.Model,
		Threshold:    cfg.Threshold,
		Top:          1,
		ReadCache:    !cfg.NoCache,
		OnCacheError: w.cacheError,
		OnAsk:        a.onAsk,
	})
	if err != nil {
		return exitError, err
	}

	// Results come in question order, Does first; the two are one when
	// they differ only in case or spacing.
	var best *search.Match
	for _, r := range results {
		if len(r.Matches) > 0 {
			output.Extend(a.stdout, r.Matches[0])
			return exitMatch, nil
		}
		if r.Best != nil && (best == nil || r.Best.Probability > best.Probability) {
			best = r.Best
		}
	}
	output.Create(a.stdout, best, cfg.Threshold)
	return exitNoMatch, nil
}

// readDraft reads draft code from a file, or stdin for "-".
func (a app) readDraft(name string) (describe.Chunk, error) {
	var b []byte
	var err error
	if name == "-" {
		name = "draft"
		b, err = io.ReadAll(a.stdin)
	} else {
		b, err = os.ReadFile(name)
	}
	if err != nil {
		return describe.Chunk{}, fmt.Errorf("--code: %w", err)
	}
	if strings.TrimSpace(string(b)) == "" {
		return describe.Chunk{}, fmt.Errorf("--code: %s is empty", name)
	}
	return describe.Chunk{Path: name, Code: string(b)}, nil
}
