// Package jev talks to TypeSafe's Jev model. Client is the seam: the HTTP
// client calls the real API, jevtest.Fake answers offline.
package jev

import (
	"context"
	"strings"
)

// State is one chunk, sent as Jev's state. It has no line numbers: they
// say nothing about what the code does, and as part of the cache key they
// would make an edit near the top of a file re-ask every chunk below it.
type State struct {
	Path string `json:"path"`
	Code string `json:"code"`
}

// Request asks every question about one state in a single call, so the
// chunk is paid for once.
type Request struct {
	Model     string
	State     State
	Questions []string // Noul instructions, see Instruction
}

type Response struct {
	Model string    // the resolved version, e.g. "jev-1.13.0"
	Nouls []float64 // probability of yes, one per Request.Questions entry
}

type Client interface {
	Ask(ctx context.Context, req Request) (Response, error)
}

// Instruction phrases a search question as a Noul statement about the
// state's code field.
func Instruction(question string) string {
	return "The `code` " + NormalizeSpace(question)
}

// NormalizeSpace collapses runs of white space and trims the ends.
func NormalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
