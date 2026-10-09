package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

const usage = `usage: jrep [flags] "question" [path...]

Searches code by what it does, using TypeSafe's Jev model.
Prints matching line ranges, best first. Exit status: 0 match, 1 no match, 2 error.

  -q "question"          extra question, repeatable; answered in the same Jev calls
  -s                     keyword pre-pass first, fewer chunks sent to Jev
  --keywords 'a|b|c'     regex for the -s pre-pass (default: words from the question)
  --refine "earlier"     retry a reworded question cheaply: search only the 300 chunks
                         that best matched an earlier question (its answers are cached)
  --threshold 0.6        minimum probability to count as a match
  --top 10               maximum results per question (0 = no limit)
  --json                 one JSON object per match
  --no-ignore            also search gitignored files
  --tests                also search test files and test data
  --docs                 also search documentation (Markdown, reStructuredText, AsciiDoc)
  --max-filesize 1M      skip larger files, listed on stderr (0 = no limit)
  --model jev-1.13.0     Jev model version
  --no-cache             don't read cached answers
  --clear-cache          delete this repo's cache and exit

Environment:
  TYPESAFE_API_KEY       API key (only needed when an answer isn't cached)
  TYPESAFE_BASE_URL      API base URL (default https://api.typesafe.ai)
  JREP_MAX_RPS           requests per second ceiling (default 320; halves on 429s)
  JREP_MAX_TPS           tokens per second ceiling (default 400000)
  JREP_CACHE_DIR         cache folder (default: jrep in the user cache folder)
`

const defaultModel = "jev-1.13.0"

// errHelp asks for the usage text on stdout and a zero exit.
var errHelp = errors.New("help requested")

type config struct {
	Questions   []string
	Paths       []string
	Prepass     bool
	Keywords    string
	Refine      string
	Threshold   float64
	Top         int
	JSON        bool
	NoIgnore    bool
	Tests       bool
	Docs        bool
	MaxFileSize int64
	Model       string
	NoCache     bool
	ClearCache  bool
}

type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ", ") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

type sizeValue struct{ n *int64 }

func (s sizeValue) String() string {
	if s.n == nil {
		return ""
	}
	return strconv.FormatInt(*s.n, 10)
}

func (s sizeValue) Set(v string) error {
	n, err := parseSize(v)
	if err != nil {
		return err
	}
	*s.n = n
	return nil
}

// parseSize reads sizes like 500, 10K, 1M, 1.5G. Units are powers of 1024.
func parseSize(v string) (int64, error) {
	v = strings.TrimSpace(strings.ToUpper(v))
	v = strings.TrimSuffix(v, "B")
	mult := 1.0
	switch {
	case strings.HasSuffix(v, "K"):
		mult = 1 << 10
	case strings.HasSuffix(v, "M"):
		mult = 1 << 20
	case strings.HasSuffix(v, "G"):
		mult = 1 << 30
	}
	if mult > 1 {
		v = v[:len(v)-1]
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("invalid size %q (try 500K, 1M or 0)", v)
	}
	return int64(f * mult), nil
}

func parseArgs(args []string) (config, error) {
	cfg := config{
		Threshold:   0.6,
		Top:         10,
		MaxFileSize: 1 << 20,
		Model:       defaultModel,
	}
	var extra stringList

	fs := flag.NewFlagSet("jrep", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Var(&extra, "q", "")
	fs.BoolVar(&cfg.Prepass, "s", false, "")
	fs.StringVar(&cfg.Keywords, "keywords", "", "")
	fs.StringVar(&cfg.Refine, "refine", "", "")
	fs.Float64Var(&cfg.Threshold, "threshold", cfg.Threshold, "")
	fs.IntVar(&cfg.Top, "top", cfg.Top, "")
	fs.BoolVar(&cfg.JSON, "json", false, "")
	fs.BoolVar(&cfg.NoIgnore, "no-ignore", false, "")
	fs.BoolVar(&cfg.Tests, "tests", false, "")
	fs.BoolVar(&cfg.Docs, "docs", false, "")
	fs.Var(sizeValue{&cfg.MaxFileSize}, "max-filesize", "")
	fs.StringVar(&cfg.Model, "model", cfg.Model, "")
	fs.BoolVar(&cfg.NoCache, "no-cache", false, "")
	fs.BoolVar(&cfg.ClearCache, "clear-cache", false, "")

	// flag stops at the first positional; keep going so flags may follow it.
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return cfg, errHelp
			}
			return cfg, err
		}
		rest := fs.Args()
		consumed := args[:len(args)-len(rest)]
		if len(consumed) > 0 && consumed[len(consumed)-1] == "--" {
			positional = append(positional, rest...)
			break
		}
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}

	if !cfg.ClearCache {
		if len(positional) == 0 {
			return cfg, errors.New("missing question")
		}
		cfg.Questions = append([]string{positional[0]}, extra...)
		positional = positional[1:]
	}
	for _, q := range cfg.Questions {
		if strings.TrimSpace(q) == "" {
			return cfg, errors.New("empty question")
		}
	}
	cfg.Paths = positional
	if len(cfg.Paths) == 0 {
		cfg.Paths = []string{"."}
	}

	if cfg.Threshold < 0 || cfg.Threshold > 1 {
		return cfg, fmt.Errorf("--threshold must be between 0 and 1, got %v", cfg.Threshold)
	}
	if cfg.Top < 0 {
		return cfg, fmt.Errorf("--top must be 0 or more, got %d", cfg.Top)
	}
	refine := false
	fs.Visit(func(f *flag.Flag) { refine = refine || f.Name == "refine" })
	if refine {
		if strings.TrimSpace(cfg.Refine) == "" {
			return cfg, errors.New("empty --refine question")
		}
		if cfg.NoCache {
			return cfg, errors.New("--refine reads cached answers for the earlier question; drop --no-cache")
		}
	}
	if cfg.Keywords != "" {
		// Without -s every chunk goes to Jev; say so rather than filter.
		if !cfg.Prepass {
			return cfg, errors.New("--keywords only applies with -s")
		}
		if _, err := regexp.Compile("(?i)" + cfg.Keywords); err != nil {
			return cfg, fmt.Errorf("--keywords: %w", err)
		}
	}
	return cfg, nil
}
