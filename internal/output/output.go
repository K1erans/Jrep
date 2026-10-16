// Package output prints search results as aligned text or JSON lines.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/K1erans/jinter/search"
)

// Text prints matches best first. A question with no match gets one plain
// line saying so, with the best candidate, so an agent can stop or rephrase.
func Text(w io.Writer, results []search.Result, threshold float64) {
	for i, r := range results {
		if len(results) > 1 {
			if i > 0 {
				fmt.Fprintln(w)
			}
			fmt.Fprintf(w, "# %s\n", r.Question)
		}
		if len(r.Matches) == 0 {
			fmt.Fprintln(w, noMatch(r, threshold))
			continue
		}
		width := 0
		for _, m := range r.Matches {
			width = max(width, len(location(m)))
		}
		for _, m := range r.Matches {
			line := fmt.Sprintf("%.2f  %-*s  %s", m.Probability, width, location(m), m.Signature)
			fmt.Fprintln(w, strings.TrimRight(line, " "))
		}
	}
}

// NoMatches prints a line for each question that matched nothing. It goes
// with JSON output, on stderr, so stdout stays machine-readable.
func NoMatches(w io.Writer, results []search.Result, threshold float64) {
	for _, r := range results {
		if len(r.Matches) == 0 {
			fmt.Fprintf(w, "%s: %s\n", r.Question, noMatch(r, threshold))
		}
	}
}

type jsonMatch struct {
	Question    string  `json:"question"`
	Path        string  `json:"path"`
	Start       int     `json:"start"`
	End         int     `json:"end"`
	Probability float64 `json:"probability"`
	Signature   string  `json:"signature"`
}

// JSON prints one object per match, one per line.
func JSON(w io.Writer, results []search.Result) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, r := range results {
		for _, m := range r.Matches {
			if err := enc.Encode(jsonMatch{r.Question, m.Path, m.Start, m.End, m.Probability, m.Signature}); err != nil {
				return err
			}
		}
	}
	return nil
}

func location(m search.Match) string {
	return fmt.Sprintf("%s:%d-%d", m.Path, m.Start, m.End)
}

func noMatch(r search.Result, threshold float64) string {
	if r.Best == nil {
		return fmt.Sprintf("no matches above %.2f (nothing searched)", threshold)
	}
	return fmt.Sprintf("no matches above %.2f (best: %.2f %s)", threshold, r.Best.Probability, location(*r.Best))
}
