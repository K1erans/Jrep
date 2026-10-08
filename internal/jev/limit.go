package jev

import (
	"context"
	"math"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Adaptive limiting. The client starts at its ceiling and halves its rate
// on 429 or 529 (at most once a second, since one overload answers many
// requests in flight). After each full second without throttling it adds
// back a tenth of the ceiling. It never drops below a sixteenth.
const (
	minShare    = 1.0 / 16
	raiseStep   = 0.1
	adjustEvery = time.Second
)

// limits shares one request limiter and one token limiter across every
// caller of a client.
type limits struct {
	mu                 sync.Mutex
	ceilReq, ceilTok   float64
	share              float64 // fraction of the ceiling in use
	lastCut, lastRaise time.Time
	now                func() time.Time // the clock; tests replace it
	req, tok           *rate.Limiter
}

func newLimits(requestsPerSecond, tokensPerSecond float64) *limits {
	// Small bursts: a fresh limiter must not send a full second's quota at once.
	return &limits{
		ceilReq: requestsPerSecond,
		ceilTok: tokensPerSecond,
		share:   1,
		now:     time.Now,
		req:     rate.NewLimiter(rate.Limit(requestsPerSecond), max(1, int(math.Ceil(requestsPerSecond/10)))),
		tok:     rate.NewLimiter(rate.Limit(tokensPerSecond), max(1, int(tokensPerSecond/5))),
	}
}

// wait blocks until one request of the given token cost may be sent. A
// request costing more than the token burst waits for it in parts, so it
// is charged in full.
func (l *limits) wait(ctx context.Context, tokens int) error {
	if err := l.req.Wait(ctx); err != nil {
		return err
	}
	for tokens > 0 {
		n := min(tokens, l.tok.Burst())
		if err := l.tok.WaitN(ctx, n); err != nil {
			return err
		}
		tokens -= n
	}
	return nil
}

// throttled records a 429 or 529.
func (l *limits) throttled() {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	if t.Sub(l.lastCut) < adjustEvery {
		return
	}
	l.lastCut = t
	l.set(max(minShare, l.share/2))
}

// succeeded records a request that wasn't throttled.
func (l *limits) succeeded() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.share >= 1 {
		return
	}
	t := l.now()
	last := l.lastCut
	if l.lastRaise.After(last) {
		last = l.lastRaise
	}
	if t.Sub(last) < adjustEvery {
		return
	}
	l.lastRaise = t
	l.set(min(1, l.share+raiseStep))
}

func (l *limits) set(share float64) {
	l.share = share
	l.req.SetLimit(rate.Limit(l.ceilReq * share))
	l.tok.SetLimit(rate.Limit(l.ceilTok * share))
}

func (l *limits) current() (requestsPerSecond, tokensPerSecond float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ceilReq * l.share, l.ceilTok * l.share
}
