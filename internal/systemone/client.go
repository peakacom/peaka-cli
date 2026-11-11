// Package systemone is a thin client for TypeSafe's System One API (model
// "Jev") and servers that speak the same protocol: one POST with a state and a
// set of questions, one JSON answer per question. It knows the limits the
// server enforces, retries what is transient, and maps the rest onto the CLI's
// exit codes.
package systemone

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/peakacom/peaka-cli/internal/build"
)

const (
	DefaultEndpoint = "https://api.typesafe.ai/v1/systemone"
	DefaultModel    = "jev-latest"

	defaultMaxAttempts = 4
	defaultGzipAbove   = 4096
	maxRetryAfter      = 30 * time.Second
	maxCriteria        = 255
)

type Client struct {
	Endpoint    string // full URL of the systemone endpoint
	Token       string // API key; sent as Bearer
	Model       string // request model; DefaultModel when empty
	HTTP        *http.Client
	Debug       io.Writer                // when set: one line per request/response, token masked, body through Redact
	MaxAttempts int                      // attempts per Do, transient failures included; 0 = 4
	GzipAbove   int                      // bodies larger than this many bytes are sent gzip-compressed; 0 = 4096, -1 disables
	Redact      func(body []byte) []byte // applied to the request body before it reaches Debug; nil = unchanged

	// Hooks so tests do not wait on the clock.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

// Question is one thing to ask about the state. Criteria is a
// map[string]string (option → description) for "choice" and a []string of
// level descriptions for "score"; "noul" has none.
type Question struct {
	Type         string `json:"type"` // "choice" | "noul" | "score"
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Choice asks the model to pick one of the criteria's keys.
func Choice(instructions string, criteria map[string]string) Question {
	return Question{Type: "choice", Instructions: instructions, Criteria: criteria}
}

// Noul asks for a 0–1 estimate of whether the instructions hold for the state.
func Noul(instructions string) Question {
	return Question{Type: "noul", Instructions: instructions}
}

type Request struct {
	Model     string              `json:"model"`
	State     any                 `json:"state"` // string, object or array
	Questions map[string]Question `json:"questions"`
}

// Answer is the model's answer to one question; which fields are set depends
// on Type.
type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Noul          float64            `json:"noul,omitempty"`
	Score         float64            `json:"score,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
}

// New returns a client for endpoint (DefaultEndpoint when empty) asking model
// (DefaultModel when empty), with an HTTP client that gives up after timeout.
func New(endpoint, token, model string, timeout time.Duration) *Client {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	if model == "" {
		model = DefaultModel
	}
	return &Client{
		Endpoint: endpoint,
		Token:    token,
		Model:    model,
		HTTP:     &http.Client{Timeout: timeout},
	}
}

// ModelName is the model Ask sends.
func (c *Client) ModelName() string {
	if c.Model == "" {
		return DefaultModel
	}
	return c.Model
}

// Host is the endpoint's host, for messages.
func (c *Client) Host() string {
	if u, err := url.Parse(c.Endpoint); err == nil && u.Host != "" {
		return u.Host
	}
	return c.Endpoint
}

// Ask sends questions about state to the client's model.
func (c *Client) Ask(ctx context.Context, state any, questions map[string]Question) (*Response, error) {
	return c.Do(ctx, Request{Model: c.ModelName(), State: state, Questions: questions})
}

// Do sends one request, retrying transient failures (429, 529, 502–504 and
// transport errors) with exponential backoff until MaxAttempts is spent or ctx
// is done. It is safe for concurrent use.
func (c *Client) Do(ctx context.Context, req Request) (*Response, error) {
	if err := validate(req); err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, &Error{Detail: "encoding the request: " + err.Error(), Err: err}
	}
	wire, encoding, err := c.encode(body)
	if err != nil {
		return nil, err
	}

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	max := c.MaxAttempts
	if max <= 0 {
		max = defaultMaxAttempts
	}

	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(wire))
		if err != nil {
			return nil, &Error{Detail: "building the request: " + err.Error(), Err: err}
		}
		hr.Header.Set("Content-Type", "application/json")
		hr.Header.Set("Accept", "application/json")
		hr.Header.Set("User-Agent", "peaka-cli/"+build.Version)
		if c.Token != "" {
			hr.Header.Set("Authorization", "Bearer "+c.Token)
		}
		if encoding != "" {
			hr.Header.Set("Content-Encoding", encoding)
		}
		c.debugRequest(req.Model, body, len(wire), encoding, attempt+1, max)

		started := c.clock()()
		resp, err := httpClient.Do(hr)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			c.debugf("< transport error (%s): %v", c.since(started), err)
			e := &Error{Retryable: true, Attempts: attempt + 1, Detail: "unreachable", Err: err}
			if attempt+1 >= max {
				return nil, e
			}
			if err := c.wait(ctx, attempt, ""); err != nil {
				return nil, err
			}
			continue
		}
		data, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		c.debugf("< %d %s (%d bytes, %s)", resp.StatusCode, resp.Header.Get("Content-Type"), len(data), c.since(started))
		if rerr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, &Error{Status: resp.StatusCode, Attempts: attempt + 1, Detail: "reading the response: " + rerr.Error(), Err: rerr}
		}

		if resp.StatusCode == http.StatusOK {
			var out Response
			if err := json.Unmarshal(data, &out); err != nil || out.Answers == nil {
				return nil, &Error{Status: http.StatusOK, Attempts: attempt + 1, Detail: "invalid response", Err: err}
			}
			return &out, nil
		}

		e := parseError(resp.StatusCode, data)
		e.Attempts = attempt + 1
		if !e.Retryable || attempt+1 >= max {
			return nil, e
		}
		if err := c.wait(ctx, attempt, resp.Header.Get("Retry-After")); err != nil {
			return nil, err
		}
	}
}

// validate applies the server's limits before anything is sent.
func validate(req Request) error {
	if len(req.Questions) == 0 {
		return &Error{Detail: "at least one question is required"}
	}
	for id, q := range req.Questions {
		switch q.Type {
		case "choice":
			n := criteriaCount(q.Criteria)
			if n < 1 || n > maxCriteria {
				return &Error{Detail: fmt.Sprintf("questions.%s.criteria: a choice needs 1–%d options, got %d", id, maxCriteria, n)}
			}
		case "noul":
			if strings.TrimSpace(q.Instructions) == "" {
				return &Error{Detail: fmt.Sprintf("questions.%s.instructions: a noul question needs instructions", id)}
			}
		}
	}
	return nil
}

func criteriaCount(v any) int {
	if v == nil {
		return 0
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map, reflect.Slice, reflect.Array:
		return rv.Len()
	}
	return 0
}

// encode gzips body when it is over the threshold; the returned encoding is
// the Content-Encoding to send ("" for none).
func (c *Client) encode(body []byte) ([]byte, string, error) {
	limit := c.GzipAbove
	if limit == 0 {
		limit = defaultGzipAbove
	}
	if limit < 0 || len(body) <= limit {
		return body, "", nil
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(body); err != nil {
		return nil, "", &Error{Detail: "compressing the request: " + err.Error(), Err: err}
	}
	if err := zw.Close(); err != nil {
		return nil, "", &Error{Detail: "compressing the request: " + err.Error(), Err: err}
	}
	return buf.Bytes(), "gzip", nil
}

// wait sleeps before the next attempt: Retry-After when the server sent one
// (capped), else 500ms·2^attempt plus up to half of that again as jitter.
func (c *Client) wait(ctx context.Context, attempt int, retryAfter string) error {
	d, ok := c.parseRetryAfter(retryAfter)
	if !ok {
		base := 500 * time.Millisecond << uint(attempt)
		d = base + time.Duration(rand.Int64N(int64(base/2)+1))
	}
	c.debugf("  retrying in %s", d)
	if c.sleep != nil {
		return c.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// parseRetryAfter reads a Retry-After header, as seconds or an HTTP date,
// capped at maxRetryAfter.
func (c *Client) parseRetryAfter(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	var d time.Duration
	if secs, err := strconv.Atoi(v); err == nil {
		d = time.Duration(secs) * time.Second
	} else if at, err := http.ParseTime(v); err == nil {
		d = at.Sub(c.clock()())
	} else {
		return 0, false
	}
	if d < 0 {
		d = 0
	}
	if d > maxRetryAfter {
		d = maxRetryAfter
	}
	return d, true
}

func (c *Client) clock() func() time.Time {
	if c.now != nil {
		return c.now
	}
	return time.Now
}

func (c *Client) since(t time.Time) time.Duration {
	return c.clock()().Sub(t).Round(time.Millisecond)
}

func (c *Client) debugRequest(model string, body []byte, wireLen int, encoding string, attempt, max int) {
	if c.Debug == nil {
		return
	}
	shown := body
	if c.Redact != nil {
		shown = c.Redact(body)
	}
	size := fmt.Sprintf("%d bytes", len(body))
	if encoding != "" {
		size = fmt.Sprintf("%d bytes, %s %d", len(body), encoding, wireLen)
	}
	c.debugf("> POST %s model=%s bearer=%s attempt=%d/%d (%s) %s", c.Endpoint, model, MaskToken(c.Token), attempt, max, size, shown)
}

func (c *Client) debugf(format string, args ...any) {
	if c.Debug == nil {
		return
	}
	fmt.Fprintf(c.Debug, format+"\n", args...)
}

// MaskToken shows a credential's shape without its value: the first and last
// four characters around stars, or only stars when it is short.
func MaskToken(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + strings.Repeat("*", len(s)-8) + s[len(s)-4:]
}
