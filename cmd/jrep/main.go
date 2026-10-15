// Command jrep searches code by what it does, using TypeSafe's Jev model.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/K1erans/jinter"
	"github.com/K1erans/jinter/cache"
	"github.com/K1erans/jinter/jev"
	"github.com/K1erans/jinter/prefilter"
	"github.com/K1erans/jinter/walk"

	"jrep/internal/output"
)

const (
	exitMatch   = 0
	exitNoMatch = 1
	exitError   = 2
)

// maxListed caps how many skipped files are named on stderr.
const maxListed = 10

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
			dir, err := cacheDir()
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
			dir, err := cacheDir()
			if err != nil {
				return err
			}
			return cache.Clear(cache.Path(dir, repo))
		},
	}
}

// cacheDir is the folder holding cache databases: $JREP_CACHE_DIR, or jrep
// inside the user cache folder (~/Library/Caches/jrep on macOS). It isn't
// jinter's default, so answers cached before jrep used jinter are kept.
func cacheDir() (string, error) {
	if d := os.Getenv("JREP_CACHE_DIR"); d != "" {
		return d, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "jrep"), nil
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

	// jinter's pre-pass error doesn't name the flag, so check first.
	if cfg.Prepass && cfg.Keywords == "" && len(prefilter.Keywords(cfg.Questions)) == 0 {
		return exitError, errors.New("-s: no keywords left in the question after dropping common words; pass --keywords")
	}
	client, err := a.client()
	if err != nil {
		return exitError, err
	}
	// jinter reports a cache that won't open and one that fails later to
	// the same callback, already explained in the first case only.
	opened := true
	engine := jinter.Engine{Client: client, OpenCache: func(repo, model string) (cache.Cache, func() error, error) {
		c, closeCache, err := a.openCache(repo, model)
		opened = err == nil
		return c, closeCache, err
	}}
	maxSize := cfg.MaxFileSize
	if maxSize == 0 {
		maxSize = -1 // jinter reads 0 as its default
	}
	warned := false
	rep, err := engine.Search(ctx, jinter.Query{
		Questions:   cfg.Questions,
		Paths:       cfg.Paths,
		Prepass:     cfg.Prepass,
		Keywords:    cfg.Keywords,
		Refine:      cfg.Refine,
		Threshold:   cfg.Threshold,
		Top:         cfg.Top,
		NoIgnore:    cfg.NoIgnore,
		Tests:       cfg.Tests,
		Docs:        cfg.Docs,
		MaxFileSize: maxSize,
		Model:       cfg.Model,
		NoCache:     cfg.NoCache,
		// A broken cache costs money, not correctness: warn once and carry on.
		OnCacheError: func(err error) {
			if warned {
				return
			}
			warned = true
			if opened {
				fmt.Fprintf(a.stderr, "jrep: warning: cache not working: %v\n", err)
			} else {
				fmt.Fprintf(a.stderr, "jrep: warning: %v\n", err)
			}
		},
		OnAsk: func(n int) {
			fmt.Fprintf(a.stderr, "jrep: asking Jev about %d %s\n", n, plural(n, "chunk", "chunks"))
		},
	})
	a.reportSkipped(rep, cfg)
	if errors.Is(err, jinter.ErrNotAsked) {
		return exitError, fmt.Errorf("--refine: no cached answers for %q: ask it first, over the same files, or drop --refine", cfg.Refine)
	}
	if err != nil {
		return exitError, err
	}
	if cfg.Prepass && rep.Chunks == 0 && cfg.Refine == "" {
		f, err := prefilter.New(cfg.Keywords, cfg.Questions)
		if err != nil {
			return exitError, fmt.Errorf("-s: %w", err)
		}
		fmt.Fprintf(a.stderr, "jrep: -s kept no chunks (keywords: %s)\n", f.Pattern())
	}
	if cfg.Refine != "" {
		fmt.Fprintf(a.stderr, "jrep: --refine: searched the %d %s that best matched %q\n", rep.Chunks, plural(rep.Chunks, "chunk", "chunks"), cfg.Refine)
	}

	if cfg.JSON {
		if err := output.JSON(a.stdout, rep.Results); err != nil {
			return exitError, err
		}
		output.NoMatches(a.stderr, rep.Results, cfg.Threshold)
	} else {
		output.Text(a.stdout, rep.Results, cfg.Threshold)
	}
	if rep.Matched() {
		return exitMatch, nil
	}
	return exitNoMatch, nil
}

// reportSkipped says on stderr which files were left out, so none are
// skipped silently. It runs after the search, so the counts come with
// errors too.
func (a app) reportSkipped(rep jinter.Report, cfg config) {
	a.reportOversize(rep.Oversize, cfg.MaxFileSize)
	if rep.TestsIncluded {
		fmt.Fprintln(a.stderr, "jrep: searched test files too, since the question is about tests")
	}
	if rep.Tests > 0 {
		fmt.Fprintf(a.stderr, "jrep: %d test %s not searched (use --tests to include)\n", rep.Tests, plural(rep.Tests, "file", "files"))
	}
	if rep.Generated > 0 {
		fmt.Fprintf(a.stderr, "jrep: %d generated %s not searched (lockfiles, source maps, minified, SVG, node_modules; name %s to include)\n",
			rep.Generated, plural(rep.Generated, "file", "files"), plural(rep.Generated, "it", "them"))
	}
	if rep.Translations > 0 {
		fmt.Fprintf(a.stderr, "jrep: %d translation %s not searched (name %s to include)\n",
			rep.Translations, plural(rep.Translations, "file", "files"), plural(rep.Translations, "it", "them"))
	}
	if rep.Docs > 0 {
		fmt.Fprintf(a.stderr, "jrep: %d %s not searched (use --docs to include)\n", rep.Docs, plural(rep.Docs, "doc", "docs"))
	}
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
