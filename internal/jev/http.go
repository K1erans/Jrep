package jev

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

const DefaultBaseURL = "https://api.typesafe.ai"

// HTTPOptions tune the real client. Zero values take the defaults below,
// which follow TypeSafe's published limits; the limits can change without
// notice, so they are settable.
type HTTPOptions struct {
	BaseURL           string        // default DefaultBaseURL
	TokensPerSecond   float64       // default 100,000
	RequestsPerSecond float64       // default 80
	MaxRetries        int           // default 6
	BaseBackoff       time.Duration // first retry delay, doubled each time; default 500 ms
	MaxBackoff        time.Duration // default 10 s
	Timeout           time.Duration // per attempt; default 60 s
}

// HTTP is the Jev client for TypeSafe's API. It is safe for concurrent use;
// all callers share one rate limiter.
type HTTP struct {
	key      string
	opts     HTTPOptions
	http     *http.Client
	requests *rate.Limiter
	tokens   *rate.Limiter
}

func NewHTTP(apiKey string, opts HTTPOptions) *HTTP {
	opts.BaseURL = strings.TrimRight(cmp.Or(opts.BaseURL, DefaultBaseURL), "/")
	opts.TokensPerSecond = cmp.Or(opts.TokensPerSecond, 100_000)
	opts.RequestsPerSecond = cmp.Or(opts.RequestsPerSecond, 80)
	opts.MaxRetries = cmp.Or(opts.MaxRetries, 6)
	opts.BaseBackoff = cmp.Or(opts.BaseBackoff, 500*time.Millisecond)
	opts.MaxBackoff = cmp.Or(opts.MaxBackoff, 10*time.Second)
	opts.Timeout = cmp.Or(opts.Timeout, 60*time.Second)
	// Small bursts: a fresh limiter must not send a full second's quota at once.
	return &HTTP{
		key:      apiKey,
		opts:     opts,
		http:     &http.Client{Timeout: opts.Timeout},
		requests: rate.NewLimiter(rate.Limit(opts.RequestsPerSecond), max(1, int(math.Ceil(opts.RequestsPerSecond/10)))),
		tokens:   rate.NewLimiter(rate.Limit(opts.TokensPerSecond), max(1, int(opts.TokensPerSecond/5))),
	}
}

type question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

type requestBody struct {
	Model     string              `json:"model"`
	State     State               `json:"state"`
	Questions map[string]question `json:"questions"`
}

type responseBody struct {
	Model   string `json:"model"`
	Answers map[string]struct {
		Type string   `json:"type"`
		Noul *float64 `json:"noul"`
	} `json:"answers"`
}

// statusError is a non-2xx reply.
type statusError struct {
	status     int
	body       string
	retryAfter time.Duration
}

func (e *statusError) Error() string {
	switch e.status {
	case http.StatusUnauthorized:
		return "TypeSafe rejected the API key (401); check TYPESAFE_API_KEY"
	case http.StatusTooManyRequests:
		return "TypeSafe rate limit (429) persisted after retries"
	case 529:
		return "TypeSafe is overloaded (529) and stayed so after retries"
	}
	return fmt.Sprintf("TypeSafe API error %d: %s", e.status, e.body)
}

func retryable(err error) bool {
	var se *statusError
	if errors.As(err, &se) {
		return se.status == http.StatusRequestTimeout || se.status == http.StatusTooManyRequests || se.status >= 500
	}
	return true // network errors and timeouts
}

func (c *HTTP) Ask(ctx context.Context, req Request) (Response, error) {
	body := requestBody{Model: req.Model, State: req.State, Questions: map[string]question{}}
	for i, q := range req.Questions {
		body.Questions[id(i)] = question{Type: "noul", Instructions: q}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	// Jev counts tokens; bytes ÷ 3 overestimates them for code, which is
	// the safe side for a client-side limit.
	tokens := min(c.tokens.Burst(), len(payload)/3+1)

	for attempt := 0; ; attempt++ {
		if err := c.requests.Wait(ctx); err != nil {
			return Response{}, err
		}
		if err := c.tokens.WaitN(ctx, tokens); err != nil {
			return Response{}, err
		}
		resp, err := c.post(ctx, payload, len(req.Questions))
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		if !retryable(err) || attempt == c.opts.MaxRetries {
			return Response{}, err
		}
		if err := sleep(ctx, c.backoff(attempt, err)); err != nil {
			return Response{}, err
		}
	}
}

func (c *HTTP) post(ctx context.Context, payload []byte, n int) (Response, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.opts.BaseURL+"/v1/systemone", bytes.NewReader(payload))
	if err != nil {
		return Response{}, err
	}
	hreq.Header.Set("Authorization", "Bearer "+c.key)
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("User-Agent", "jrep")
	hresp, err := c.http.Do(hreq)
	if err != nil {
		return Response{}, err
	}
	defer hresp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(hresp.Body, 1<<20))
	if err != nil {
		return Response{}, err
	}
	if hresp.StatusCode/100 != 2 {
		return Response{}, &statusError{
			status:     hresp.StatusCode,
			body:       strings.TrimSpace(string(raw)),
			retryAfter: parseRetryAfter(hresp.Header.Get("Retry-After")),
		}
	}
	var rb responseBody
	if err := json.Unmarshal(raw, &rb); err != nil {
		return Response{}, fmt.Errorf("reading TypeSafe response: %w", err)
	}
	out := Response{Model: rb.Model, Nouls: make([]float64, n)}
	for i := range n {
		a, ok := rb.Answers[id(i)]
		if !ok || a.Noul == nil {
			return Response{}, fmt.Errorf("TypeSafe response has no noul answer for %s", id(i))
		}
		out.Nouls[i] = *a.Noul
	}
	return out, nil
}

func id(i int) string { return "q" + strconv.Itoa(i) }

// backoff doubles from BaseBackoff with jitter, capped at MaxBackoff. A
// retry-after header wins when it asks for longer.
func (c *HTTP) backoff(attempt int, err error) time.Duration {
	d := min(c.opts.BaseBackoff<<attempt, c.opts.MaxBackoff)
	d = d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
	var se *statusError
	if errors.As(err, &se) && se.retryAfter > d {
		d = se.retryAfter
	}
	return d
}

// parseRetryAfter reads delay-seconds or an HTTP date.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if s, err := strconv.ParseFloat(v, 64); err == nil && s > 0 {
		return time.Duration(s * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
