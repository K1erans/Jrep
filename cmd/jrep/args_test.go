package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseArgsDefaults(t *testing.T) {
	cfg, err := parseArgs([]string{"retries failed payments"})
	if err != nil {
		t.Fatal(err)
	}
	want := config{
		Questions:   []string{"retries failed payments"},
		Paths:       []string{"."},
		Threshold:   0.6,
		Top:         10,
		MaxFileSize: 1 << 20,
		Model:       "jev-1.13.0",
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("got %+v\nwant %+v", cfg, want)
	}
}

func TestParseArgsFlagsAfterPositionals(t *testing.T) {
	cfg, err := parseArgs([]string{
		"retries failed payments", "src/", "-s", "--threshold", "0.8",
		"-q", "sends email", "lib/", "-q", "logs errors", "--json",
		"--top", "3", "--max-filesize", "2M", "--no-ignore", "--tests", "--docs", "--no-cache",
		"--keywords", "retry|backoff", "--model", "jev-latest",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := config{
		Questions:   []string{"retries failed payments", "sends email", "logs errors"},
		Paths:       []string{"src/", "lib/"},
		Prepass:     true,
		Keywords:    "retry|backoff",
		Threshold:   0.8,
		Top:         3,
		JSON:        true,
		NoIgnore:    true,
		Tests:       true,
		Docs:        true,
		MaxFileSize: 2 << 20,
		Model:       "jev-latest",
		NoCache:     true,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("got %+v\nwant %+v", cfg, want)
	}
}

func TestParseArgsDoubleDashEndsFlags(t *testing.T) {
	cfg, err := parseArgs([]string{"--", "-weird question", "-dir"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Questions[0] != "-weird question" || cfg.Paths[0] != "-dir" {
		t.Fatalf("got %+v", cfg)
	}
}

func TestParseArgsClearCacheTakesOnlyPaths(t *testing.T) {
	cfg, err := parseArgs([]string{"--clear-cache", "src/"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ClearCache || len(cfg.Questions) != 0 || !reflect.DeepEqual(cfg.Paths, []string{"src/"}) {
		t.Fatalf("got %+v", cfg)
	}
}

func TestParseArgsMaxFileSize(t *testing.T) {
	cases := map[string]int64{"0": 0, "500": 500, "10K": 10 << 10, "1m": 1 << 20, "1G": 1 << 30, "1.5M": 3 << 19}
	for in, want := range cases {
		cfg, err := parseArgs([]string{"--max-filesize", in, "q"})
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if cfg.MaxFileSize != want {
			t.Errorf("%s: got %d, want %d", in, cfg.MaxFileSize, want)
		}
	}
}

func TestParseArgsErrors(t *testing.T) {
	cases := map[string][]string{
		"question":  {},
		"threshold": {"--threshold", "1.5", "q"},
		"top":       {"--top", "-1", "q"},
		"size":      {"--max-filesize", "lots", "q"},
		"empty":     {"   "},
		"unknown":   {"--frobnicate", "q"},
		"keywords":  {"-s", "--keywords", "(", "q"},
		"needs -s":  {"--keywords", "retry", "q"},
	}
	for name, args := range cases {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("%s: expected an error for %q", name, strings.Join(args, " "))
		}
	}
}

func TestRefineNamesAnEarlierQuestionAndNeedsTheCache(t *testing.T) {
	cfg, err := parseArgs([]string{"removes blur from images", "--refine", "strips image resize params"})
	if err != nil || cfg.Refine != "strips image resize params" {
		t.Fatalf("got %q, %v", cfg.Refine, err)
	}
	if _, err := parseArgs([]string{"q", "--refine", " "}); err == nil {
		t.Fatal("an empty --refine question should be rejected")
	}
	if _, err := parseArgs([]string{"q", "--refine", "old", "--no-cache"}); err == nil || !strings.Contains(err.Error(), "--no-cache") {
		t.Fatalf("--refine reads cached answers, so --no-cache should be rejected: %v", err)
	}
}
