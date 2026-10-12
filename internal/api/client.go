// Package api is the thin HTTP layer under every command: identity header,
// user agent, problem+json parsing, retries on retryable problems.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/peakacom/peaka-cli/internal/build"
)

type Client struct {
	BaseURL string // ends with /api/v1
	Token   string // API key or access token; sent as Bearer
	HTTP    *http.Client
	Debug   io.Writer // when set, request/response lines go here (secrets masked)
	DryRun  io.Writer // when set, writing requests are printed here as JSON and not sent
	Client  string    // "cli" or "mcp", for X-Peaka-Client
}

// RawBody is a request body sent as is, with its own content type (multipart uploads).
type RawBody struct {
	ContentType string
	Data        []byte
}

type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

func New(baseURL, token string, timeout time.Duration) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: timeout},
		Client:  "cli",
	}
}

func (c *Client) userAgent() string {
	return fmt.Sprintf("peaka-cli/%s (%s; %s; %s)", build.Version, runtime.GOOS, runtime.GOARCH, c.Client)
}

// Do sends one request. path is relative to BaseURL (leading slash optional).
// body: nil, []byte, io.Reader, or any value marshalled as JSON.
// out: nil, *[]byte, or a pointer to decode JSON into.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body any, out any) (*Response, error) {
	u := c.BaseURL + "/" + strings.TrimLeft(path, "/")
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var payload []byte
	contentType := ""
	switch b := body.(type) {
	case nil:
	case *RawBody:
		payload, contentType = b.Data, b.ContentType
	case []byte:
		payload, contentType = b, "application/json"
	case io.Reader:
		var err error
		if payload, err = io.ReadAll(b); err != nil {
			return nil, err
		}
		contentType = "application/json"
	default:
		var err error
		if payload, err = json.Marshal(b); err != nil {
			return nil, err
		}
		contentType = "application/json"
	}

	if c.DryRun != nil && method != http.MethodGet && method != http.MethodHead {
		dry := map[string]any{"dryRun": true, "method": method, "url": u}
		if len(payload) > 0 {
			if json.Valid(payload) {
				dry["body"] = json.RawMessage(payload)
			} else {
				dry["body"] = fmt.Sprintf("<%d bytes, %s>", len(payload), contentType)
			}
		}
		enc := json.NewEncoder(c.DryRun)
		enc.SetIndent("", "  ")
		_ = enc.Encode(dry)
		return &Response{Status: 0, Header: http.Header{}}, nil
	}

	var last *Response
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json, application/problem+json")
		req.Header.Set("User-Agent", c.userAgent())
		req.Header.Set("X-Peaka-Client", c.Client)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
		c.debugf("> %s %s", method, u)
		started := time.Now()
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, Exit(ExitInterrupted, "interrupted")
			}
			return nil, &Problem{Status: 0, Code: "UPSTREAM_UNAVAILABLE", Detail: err.Error(), Retryable: true}
		}
		data, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		c.debugf("< %d %s (%d bytes, %s)", resp.StatusCode, resp.Header.Get("Content-Type"), len(data), time.Since(started).Round(time.Millisecond))
		if rerr != nil {
			return nil, rerr
		}
		last = &Response{Status: resp.StatusCode, Header: resp.Header, Body: data}
		if resp.StatusCode < 400 {
			break
		}
		p := parseProblem(resp, data)
		// Only idempotent reads are retried, and only when the API says so.
		if p.Retryable && (method == http.MethodGet || method == http.MethodHead) && attempt < 2 {
			wait := time.Duration(p.RetryAfter) * time.Second
			if wait <= 0 {
				wait = time.Duration(1<<attempt) * time.Second
			}
			c.debugf("  retryable, waiting %s", wait)
			select {
			case <-time.After(wait):
				continue
			case <-ctx.Done():
				return nil, Exit(ExitInterrupted, "interrupted")
			}
		}
		return last, p
	}
	if out != nil && len(last.Body) > 0 {
		switch o := out.(type) {
		case *[]byte:
			*o = last.Body
		default:
			if err := json.Unmarshal(last.Body, out); err != nil {
				return last, fmt.Errorf("unexpected response body (%s): %w", last.Header.Get("Content-Type"), err)
			}
		}
	}
	return last, nil
}

// Get is Do(GET) decoding into out.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	_, err := c.Do(ctx, http.MethodGet, path, query, nil, out)
	return err
}

func parseProblem(resp *http.Response, data []byte) *Problem {
	p := &Problem{Status: resp.StatusCode, Raw: append([]byte(nil), data...)}
	if json.Valid(data) {
		_ = json.Unmarshal(data, p)
		if p.Code == "" {
			// not a problem+json body (an older gateway, a proxy): keep its message, not the whole object
			var generic struct {
				Message string `json:"message"`
				Error   string `json:"error"`
				Path    string `json:"path"`
			}
			if json.Unmarshal(data, &generic) == nil {
				p.Detail = firstNonEmptyString(generic.Message, generic.Error)
				if p.Instance == "" {
					p.Instance = generic.Path
				}
			}
		}
	}
	if p.Status == 0 {
		p.Status = resp.StatusCode
	}
	if p.Code == "" {
		switch resp.StatusCode {
		case 401:
			p.Code = "AUTHENTICATION_REQUIRED"
		case 403:
			p.Code = "PERMISSION_REQUIRED"
		case 404:
			p.Code = "NOT_FOUND"
		case 429:
			p.Code, p.Retryable = "RATE_LIMITED", true
		case 502, 503, 504:
			p.Code, p.Retryable = "UPSTREAM_UNAVAILABLE", true
		default:
			p.Code = "REQUEST_FAILED"
		}
		if p.Detail == "" {
			p.Detail = strings.TrimSpace(string(firstLine(data)))
		}
	}
	if p.RetryAfter == 0 {
		if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
			p.RetryAfter = ra
		}
	}
	if p.TraceID == "" {
		p.TraceID = resp.Header.Get("X-Trace-Id")
	}
	return p
}

func firstLine(b []byte) []byte {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[:i]
	}
	if len(b) > 200 {
		b = b[:200]
	}
	return b
}

func (c *Client) debugf(format string, args ...any) {
	if c.Debug == nil {
		return
	}
	fmt.Fprintf(c.Debug, format+"\n", args...)
}

// IsTerminal reports whether f is a terminal (used for default formats and prompts).
// IsTerminal says whether f is an interactive terminal. A character-device
// check is not enough: /dev/null is one too, and a prompt read from it ends
// at once.
func IsTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

func firstNonEmptyString(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}
