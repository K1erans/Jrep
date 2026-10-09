// Command jrep searches code by what it does, using TypeSafe's Jev model.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"jrep/internal/cache"
	"jrep/internal/chunk"
	"jrep/internal/jev"
	"jrep/internal/output"
	"jrep/internal/prefilter"
	"jrep/internal/search"
	"jrep/internal/walk"
)

const (
	exitMatch   = 0
	exitNoMatch = 1
	exitError   = 2
)

// maxListed caps how many skipped files are named on stderr.
const maxListed = 10

// refineTop is how many chunks --refine searches. Replaying 24 rewordings
// of 12 miniflux tasks, the right chunk was always in the earlier
// question's top 11 and kept its rank with only the top 30 searched; 300
// leaves room for bigger repos at about 9% of that 3,400-chunk scan.
const refineTop = 300

// app holds what main wires up for real and tests replace.
type app struct {
	stdout, stderr io.Writer
	client         func() (jev.Client, error)
	openCache      func(repo, model string) (cache.Cache, func() error, error)
	clearCache     func(repo string) error
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := realApp(os.Stdout, os.Stderr).run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}

func realApp(stdout, stderr io.Writer) app {
	return app{
		stdout: stdout,
		stderr: stderr,
		client: func() (jev.Client, error) {
			key := os.Getenv("TYPESAFE_API_KEY")
			if key == "" {
				return noKey{}, nil
			}
			opts := jev.HTTPOptions{BaseURL: os.Getenv("TYPESAFE_BASE_URL")}
			var err error
			if opts.RequestsPerSecond, err = envRate("JREP_MAX_RPS"); err != nil {
				return nil, err
			}
			if opts.TokensPerSecond, err = envRate("JREP_MAX_TPS"); err != nil {
				return nil, err
			}
			return jev.NewHTTP(key, opts), nil
		},
		openCache: func(repo, model string) (cache.Cache, func() error, error) {
			dir, err := cache.Dir()
			if err != nil {
				return nil, nil, err
			}
			c, err := cache.Open(cache.Path(dir, repo), model)
			if err != nil {
				return nil, nil, err
			}
			return c, c.Close, nil
		},
		clearCache: func(repo string) error {
			dir, err := cache.Dir()
			if err != nil {
				return err
			}
			return cache.Clear(cache.Path(dir, repo))
		},
	}
}

// noKey stands in for the Jev client when no API key is set, so searches
// answered entirely from the cache still work.
type noKey struct{}

func (noKey) Ask(context.Context, jev.Request) (jev.Response, error) {
	return jev.Response{}, errors.New("TYPESAFE_API_KEY is not set")
}

func (a app) run(ctx context.Context, args []string) int {
	cfg, err := parseArgs(args)
	if errors.Is(err, errHelp) {
		fmt.Fprint(a.stdout, usage)
		return exitMatch
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "jrep: %v\n\n%s", err, usage)
		return exitError
	}
	code, err := a.search(ctx, cfg)
	if err != nil {
		fmt.Fprintf(a.stderr, "jrep: %v\n", err)
		return exitError
	}
	return code
}

func (a app) search(ctx context.Context, cfg config) (int, error) {
	if cfg.ClearCache {
		repo, err := walk.Repo(cfg.Paths[0])
		if err != nil {
			return exitError, err
		}
		return exitMatch, a.clearCache(repo)
	}

	files, err := walk.Walk(cfg.Paths, walk.Options{NoIgnore: cfg.NoIgnore, Tests: cfg.Tests, Docs: cfg.Docs, MaxFileSize: cfg.MaxFileSize})
	if err != nil {
		return exitError, err
	}
	a.reportOversize(files.Oversize, cfg.MaxFileSize)
	if files.Tests > 0 {
		fmt.Fprintf(a.stderr, "jrep: %d test %s not searched (use --tests to include)\n", files.Tests, plural(files.Tests, "file", "files"))
	}
	if files.Generated > 0 {
		fmt.Fprintf(a.stderr, "jrep: %d generated %s not searched (lockfiles, source maps, minified, SVG, node_modules; name %s to include)\n",
			files.Generated, plural(files.Generated, "file", "files"), plural(files.Generated, "it", "them"))
	}
	if files.Translations > 0 {
		fmt.Fprintf(a.stderr, "jrep: %d translation %s not searched (name %s to include)\n",
			files.Translations, plural(files.Translations, "file", "files"), plural(files.Translations, "it", "them"))
	}
	if files.Docs > 0 {
		fmt.Fprintf(a.stderr, "jrep: %d %s not searched (use --docs to include)\n", files.Docs, plural(files.Docs, "doc", "docs"))
	}

	// A broken cache costs money, not correctness: warn and carry on.
	store, closeStore, err := a.openCache(files.Repo, cfg.Model)
	if err != nil {
		fmt.Fprintf(a.stderr, "jrep: warning: cache unavailable, answers won't be saved: %v\n", err)
		store, closeStore = cache.NewMemory(), func() error { return nil }
	}
	defer closeStore()
	warned := false
	onCacheError := func(err error) {
		if !warned {
			warned = true
			fmt.Fprintf(a.stderr, "jrep: warning: cache not working: %v\n", err)
		}
	}

	chunks, err := search.Load(files.Files, search.LoadOptions{
		Chunk:        chunk.Default,
		Store:        store,
		ReadCache:    !cfg.NoCache,
		OnCacheError: onCacheError,
	})
	if err != nil {
		return exitError, err
	}
	if cfg.Prepass {
		f, err := prefilter.New(cfg.Keywords, cfg.Questions)
		if err != nil {
			return exitError, fmt.Errorf("-s: %w", err)
		}
		kept := chunks[:0:0]
		for _, c := range chunks {
			if f.Keep(c.Code) {
				kept = append(kept, c)
			}
		}
		if len(kept) == 0 {
			fmt.Fprintf(a.stderr, "jrep: -s kept 0 of %d chunks (keywords: %s)\n", len(chunks), f.Pattern())
		}
		chunks = kept
	}
	if cfg.Refine != "" {
		chunks, err = search.Narrow(chunks, store, cfg.Model, cfg.Refine, refineTop)
		if errors.Is(err, search.ErrNotAsked) {
			return exitError, fmt.Errorf("--refine: no cached answers for %q: ask it first, over the same files, or drop --refine", cfg.Refine)
		}
		if err != nil {
			return exitError, fmt.Errorf("--refine: %w", err)
		}
		fmt.Fprintf(a.stderr, "jrep: --refine: searching the %d %s that best matched %q\n", len(chunks), plural(len(chunks), "chunk", "chunks"), cfg.Refine)
	}

	client, err := a.client()
	if err != nil {
		return exitError, err
	}
	results, err := search.Run(ctx, chunks, client, store, search.Options{
		Questions:    cfg.Questions,
		Model:        cfg.Model,
		Threshold:    cfg.Threshold,
		Top:          cfg.Top,
		ReadCache:    !cfg.NoCache,
		OnCacheError: onCacheError,
		OnAsk: func(n int) {
			fmt.Fprintf(a.stderr, "jrep: asking Jev about %d %s\n", n, plural(n, "chunk", "chunks"))
		},
	})
	if err != nil {
		return exitError, err
	}

	if cfg.JSON {
		if err := output.JSON(a.stdout, results); err != nil {
			return exitError, err
		}
		output.NoMatches(a.stderr, results, cfg.Threshold)
	} else {
		output.Text(a.stdout, results, cfg.Threshold)
	}
	for _, r := range results {
		if len(r.Matches) > 0 {
			return exitMatch, nil
		}
	}
	return exitNoMatch, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// reportOversize names skipped files on stderr so they are never skipped
// silently.
func (a app) reportOversize(files []walk.File, limit int64) {
	if len(files) == 0 {
		return
	}
	var names []string
	for i, f := range files {
		if i == maxListed {
			names = append(names, fmt.Sprintf("and %d more", len(files)-maxListed))
			break
		}
		names = append(names, f.Path)
	}
	fmt.Fprintf(a.stderr, "jrep: skipped %d %s over %s: %s (use --max-filesize 0 to include)\n",
		len(files), plural(len(files), "file", "files"), formatSize(limit), strings.Join(names, ", "))
}

// envRate reads an optional positive rate limit override.
func envRate(name string) (float64, error) {
	v := os.Getenv(name)
	if v == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("%s must be a positive number, got %q", name, v)
	}
	return f, nil
}

func formatSize(n int64) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return fmt.Sprintf("%d GB", n>>30)
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MB", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}
