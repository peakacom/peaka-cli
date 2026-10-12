// Package trino speaks Trino's HTTP protocol to Peaka's dbc endpoint — the
// same path the JDBC, Python and Node drivers use. It is deliberately thin:
// the REPL needs the stats, warnings, session headers and cancellation that
// database/sql hides.
package trino

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Client struct {
	Endpoint string // e.g. https://dbc.peaka.studio:4567
	HTTP     *http.Client
	User     string            // X-Trino-User; informational on dbc
	Source   string            // X-Trino-Source
	Extra    map[string]string // X-Trino-Extra-Credential: peakaKey, peakaProject
	Debug    io.Writer

	// Session state, updated from response headers (USE, SET SESSION, PREPARE).
	mu       sync.Mutex
	Catalog  string
	Schema   string
	Path     string
	Session  map[string]string
	Prepared map[string]string
}

// Column is one result column.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Stats is the subset of Trino's query statistics the CLI shows.
type Stats struct {
	State              string  `json:"state"`
	Queued             bool    `json:"queued"`
	Scheduled          bool    `json:"scheduled"`
	ProgressPercentage float64 `json:"progressPercentage"`
	ElapsedTimeMillis  int64   `json:"elapsedTimeMillis"`
	QueuedTimeMillis   int64   `json:"queuedTimeMillis"`
	CPUTimeMillis      int64   `json:"cpuTimeMillis"`
	ProcessedRows      int64   `json:"processedRows"`
	ProcessedBytes     int64   `json:"processedBytes"`
	PhysicalInputBytes int64   `json:"physicalInputBytes"`
	PeakMemoryBytes    int64   `json:"peakMemoryBytes"`
	TotalSplits        int64   `json:"totalSplits"`
	CompletedSplits    int64   `json:"completedSplits"`
	RunningSplits      int64   `json:"runningSplits"`
	QueuedSplits       int64   `json:"queuedSplits"`
	Nodes              int64   `json:"nodes"`
}

// Warning is a Trino warning attached to a result page.
type Warning struct {
	Code    map[string]any `json:"warningCode"`
	Message string         `json:"message"`
}

type page struct {
	ID          string            `json:"id"`
	InfoURI     string            `json:"infoUri"`
	NextURI     string            `json:"nextUri"`
	Columns     []Column          `json:"columns"`
	Data        []json.RawMessage `json:"data"`
	Stats       Stats             `json:"stats"`
	Error       *QueryError       `json:"error"`
	Warnings    []Warning         `json:"warnings"`
	UpdateType  string            `json:"updateType"`
	UpdateCount *int64            `json:"updateCount"`
}

// Query is one running statement; Next pages through its results.
type Query struct {
	c           *Client
	ID          string
	InfoURI     string
	Columns     []Column
	Stats       Stats
	Warnings    []Warning
	UpdateType  string
	UpdateCount *int64

	nextURI string
	done    bool
	started time.Time
}

// Execute starts a statement. The first page is fetched; call Next for rows.
func (c *Client) Execute(ctx context.Context, statement string) (*Query, error) {
	q := &Query{c: c, started: time.Now()}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Endpoint, "/")+"/v1/statement", strings.NewReader(statement))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/plain")
	c.setHeaders(req)
	p, err := c.send(ctx, req)
	if err != nil {
		return nil, err
	}
	q.absorb(p)
	return q, nil
}

// Next fetches the following page and returns its rows (possibly none while
// the query is queued). done is true once the query has finished; a failed
// query returns its error here.
func (q *Query) Next(ctx context.Context) (rows [][]any, done bool, err error) {
	if q.done {
		return nil, true, nil
	}
	if q.nextURI == "" {
		q.done = true
		return nil, true, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, q.nextURI, nil)
	if err != nil {
		return nil, false, err
	}
	q.c.setHeaders(req)
	p, err := q.c.send(ctx, req)
	if err != nil {
		return nil, false, err
	}
	q.absorb(p)
	if p.Error != nil {
		q.done = true
		p.Error.QueryID = q.ID
		return nil, true, p.Error
	}
	rows, err = decodeRows(p.Data)
	if err != nil {
		return nil, false, err
	}
	if q.nextURI == "" {
		q.done = true
	}
	return rows, q.done, nil
}

// Cancel asks the server to stop the query (DELETE nextUri). Safe to call twice.
func (q *Query) Cancel() error {
	if q.done || q.nextURI == "" {
		return nil
	}
	q.done = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, q.nextURI, nil)
	if err != nil {
		return err
	}
	q.c.setHeaders(req)
	resp, err := q.c.HTTP.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	q.c.debugf("< DELETE %d", resp.StatusCode)
	return nil
}

// Elapsed is wall time since Execute, measured by the client.
func (q *Query) Elapsed() time.Duration { return time.Since(q.started) }

func (q *Query) absorb(p *page) {
	if p.ID != "" {
		q.ID = p.ID
	}
	if p.InfoURI != "" {
		q.InfoURI = p.InfoURI
	}
	if len(p.Columns) > 0 && len(q.Columns) == 0 {
		q.Columns = p.Columns
	}
	q.Stats = p.Stats
	q.Warnings = append(q.Warnings, p.Warnings...)
	if p.UpdateType != "" {
		q.UpdateType = p.UpdateType
	}
	if p.UpdateCount != nil {
		q.UpdateCount = p.UpdateCount
	}
	q.nextURI = p.NextURI
}

func decodeRows(raw []json.RawMessage) ([][]any, error) {
	rows := make([][]any, 0, len(raw))
	for _, r := range raw {
		dec := json.NewDecoder(bytes.NewReader(r))
		dec.UseNumber()
		var row []any
		if err := dec.Decode(&row); err != nil {
			return nil, fmt.Errorf("decoding a result row: %w", err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (c *Client) setHeaders(req *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	user := c.User
	if user == "" {
		user = "peaka-cli"
	}
	req.Header.Set("X-Trino-User", user)
	if c.Source != "" {
		req.Header.Set("X-Trino-Source", c.Source)
	}
	req.Header.Set("X-Trino-Client-Capabilities", "PARAMETRIC_DATETIME")
	if len(c.Extra) > 0 {
		parts := make([]string, 0, len(c.Extra))
		for k, v := range c.Extra {
			parts = append(parts, k+"="+v)
		}
		req.Header.Set("X-Trino-Extra-Credential", strings.Join(parts, ", "))
	}
	if c.Catalog != "" {
		req.Header.Set("X-Trino-Catalog", c.Catalog)
	}
	if c.Schema != "" {
		req.Header.Set("X-Trino-Schema", c.Schema)
	}
	if c.Path != "" {
		req.Header.Set("X-Trino-Path", c.Path)
	}
	if len(c.Session) > 0 {
		parts := make([]string, 0, len(c.Session))
		for k, v := range c.Session {
			parts = append(parts, k+"="+url.QueryEscape(v))
		}
		req.Header.Set("X-Trino-Session", strings.Join(parts, ","))
	}
	for name, stmt := range c.Prepared {
		req.Header.Add("X-Trino-Prepared-Statement", name+"="+url.QueryEscape(stmt))
	}
}

// send performs one protocol request, retrying on 503 as the protocol asks,
// updating session state from the response headers.
func (c *Client) send(ctx context.Context, req *http.Request) (*page, error) {
	var body []byte
	if req.Body != nil && req.Method == http.MethodPost {
		body, _ = io.ReadAll(req.Body)
	}
	backoff := 100 * time.Millisecond
	for attempt := 0; ; attempt++ {
		if body != nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
		}
		c.debugf("> %s %s", req.Method, req.URL)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, &TransportError{Err: err}
		}
		data, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		c.debugf("< %d %s (%d bytes)", resp.StatusCode, resp.Header.Get("Content-Type"), len(data))
		if rerr != nil {
			return nil, rerr
		}
		if resp.StatusCode == http.StatusServiceUnavailable && attempt < 20 {
			// Trino: the coordinator is busy; retry the same URI.
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if backoff < 2*time.Second {
				backoff *= 2
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return nil, &HTTPError{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: data}
		}
		c.applyHeaders(resp.Header)
		var p page
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("unexpected response from %s: %w", req.URL.Host, err)
		}
		return &p, nil
	}
}

func (c *Client) applyHeaders(h http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v := h.Get("X-Trino-Set-Catalog"); v != "" {
		c.Catalog = v
	}
	if v := h.Get("X-Trino-Set-Schema"); v != "" {
		c.Schema = v
	}
	if v := h.Get("X-Trino-Set-Path"); v != "" {
		c.Path = v
	}
	for _, kv := range h.Values("X-Trino-Set-Session") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			if c.Session == nil {
				c.Session = map[string]string{}
			}
			if dv, err := url.QueryUnescape(v); err == nil {
				v = dv
			}
			c.Session[k] = v
		}
	}
	for _, k := range h.Values("X-Trino-Clear-Session") {
		delete(c.Session, k)
	}
	for _, kv := range h.Values("X-Trino-Added-Prepare") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			if c.Prepared == nil {
				c.Prepared = map[string]string{}
			}
			if dv, err := url.QueryUnescape(v); err == nil {
				v = dv
			}
			c.Prepared[k] = v
		}
	}
	for _, k := range h.Values("X-Trino-Deallocated-Prepare") {
		delete(c.Prepared, k)
	}
}

func (c *Client) debugf(format string, args ...any) {
	if c.Debug != nil {
		fmt.Fprintf(c.Debug, format+"\n", args...)
	}
}
