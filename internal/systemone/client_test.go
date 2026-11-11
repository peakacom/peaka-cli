package systemone

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/peakacom/peaka-cli/internal/api"
)

const testToken = "sk-typesafe-0123456789abcdef"

// newTestClient wires a client to srv with a sleep hook that records the
// backoff instead of waiting for it.
func newTestClient(t *testing.T, srv *httptest.Server) (*Client, *[]time.Duration) {
	t.Helper()
	var waits []time.Duration
	c := New(srv.URL, testToken, "jev-test", 5*time.Second)
	c.sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	return c, &waits
}

func okAnswer() string {
	return `{"model":"jev-1.14.0","answers":{"label":{"type":"choice","choice":"bug","confidence":0.93,"probabilities":{"bug":0.93,"feature":0.07}},"spam":{"type":"noul","noul":0.02}},"usage":{"input_tokens":123,"output_tokens":0}}`
}

func sampleQuestions() map[string]Question {
	return map[string]Question{
		"label": Choice("Classify the issue", map[string]string{"bug": "a defect", "feature": "a request"}),
		"spam":  Noul("Is this spam?"),
	}
}

func TestAskSendsHeadersAndBodyAndDecodesResponse(t *testing.T) {
	var got struct {
		header http.Header
		body   []byte
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.header = r.Header.Clone()
		got.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, okAnswer())
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv)

	resp, err := c.Ask(context.Background(), map[string]any{"title": "crash on save"}, sampleQuestions())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if a := got.header.Get("Authorization"); a != "Bearer "+testToken {
		t.Errorf("Authorization = %q", a)
	}
	if ct := got.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if ac := got.header.Get("Accept"); ac != "application/json" {
		t.Errorf("Accept = %q", ac)
	}
	if ua := got.header.Get("User-Agent"); !strings.HasPrefix(ua, "peaka-cli/") {
		t.Errorf("User-Agent = %q", ua)
	}
	if ce := got.header.Get("Content-Encoding"); ce != "" {
		t.Errorf("small body should not be compressed, Content-Encoding = %q", ce)
	}

	var sent Request
	if err := json.Unmarshal(got.body, &sent); err != nil {
		t.Fatalf("body is not a Request: %v\n%s", err, got.body)
	}
	if sent.Model != "jev-test" {
		t.Errorf("model = %q", sent.Model)
	}
	if st, ok := sent.State.(map[string]any); !ok || st["title"] != "crash on save" {
		t.Errorf("state = %#v", sent.State)
	}
	label := sent.Questions["label"]
	if label.Type != "choice" || label.Instructions != "Classify the issue" {
		t.Errorf("label question = %+v", label)
	}
	if crit, ok := label.Criteria.(map[string]any); !ok || crit["bug"] != "a defect" || len(crit) != 2 {
		t.Errorf("label criteria = %#v", label.Criteria)
	}
	spam := sent.Questions["spam"]
	if spam.Type != "noul" || spam.Criteria != nil {
		t.Errorf("spam question = %+v", spam)
	}
	if bytes.Contains(got.body, []byte(`"criteria":null`)) {
		t.Errorf("noul question should omit criteria: %s", got.body)
	}

	if resp.Model != "jev-1.14.0" {
		t.Errorf("response model = %q", resp.Model)
	}
	a := resp.Answers["label"]
	if a.Type != "choice" || a.Choice != "bug" || a.Confidence != 0.93 || a.Probabilities["feature"] != 0.07 {
		t.Errorf("label answer = %+v", a)
	}
	if n := resp.Answers["spam"]; n.Type != "noul" || n.Noul != 0.02 {
		t.Errorf("spam answer = %+v", n)
	}
	if resp.Usage.InputTokens != 123 || resp.Usage.OutputTokens != 0 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestGzipAboveThreshold(t *testing.T) {
	var encoding string
	var inflated []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		encoding = r.Header.Get("Content-Encoding")
		raw, _ := io.ReadAll(r.Body)
		if encoding == "gzip" {
			zr, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			inflated, _ = io.ReadAll(zr)
		} else {
			inflated = raw
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, okAnswer())
	}))
	defer srv.Close()

	big := strings.Repeat("the quick brown fox jumps over the lazy dog ", 200) // ~8.8 KB
	c, _ := newTestClient(t, srv)
	c.GzipAbove = 1024
	if _, err := c.Ask(context.Background(), big, sampleQuestions()); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if encoding != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", encoding)
	}
	var sent Request
	if err := json.Unmarshal(inflated, &sent); err != nil {
		t.Fatalf("inflated body is not JSON: %v", err)
	}
	if sent.State != big || sent.Model != "jev-test" || len(sent.Questions) != 2 {
		t.Errorf("inflated request differs: model=%q questions=%d state ok=%v", sent.Model, len(sent.Questions), sent.State == big)
	}

	// Under the threshold the same request goes as plain JSON.
	c.GzipAbove = 1 << 20
	if _, err := c.Ask(context.Background(), big, sampleQuestions()); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if encoding != "" {
		t.Errorf("Content-Encoding = %q under the threshold", encoding)
	}
	if !json.Valid(inflated) {
		t.Errorf("plain body is not JSON: %s", inflated)
	}

	// -1 disables compression regardless of size.
	c.GzipAbove = -1
	if _, err := c.Ask(context.Background(), big, sampleQuestions()); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if encoding != "" {
		t.Errorf("Content-Encoding = %q with GzipAbove=-1", encoding)
	}
}

func TestRetryAfterThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"detail":"rate limited"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, okAnswer())
	}))
	defer srv.Close()
	c, waits := newTestClient(t, srv)

	resp, err := c.Ask(context.Background(), "x", sampleQuestions())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if len(*waits) != 1 || (*waits)[0] != 0 {
		t.Errorf("waits = %v, want [0] from Retry-After: 0", *waits)
	}
	if resp.Answers["label"].Choice != "bug" {
		t.Errorf("answer after retry = %+v", resp.Answers["label"])
	}
}

func TestOverloadedExhaustsAttempts(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(529)
		io.WriteString(w, `{"detail":"overloaded"}`)
	}))
	defer srv.Close()
	c, waits := newTestClient(t, srv)

	_, err := c.Ask(context.Background(), "x", sampleQuestions())
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %T %v, want *Error", err, err)
	}
	if e.Status != 529 || !e.Retryable || e.Attempts != 4 {
		t.Errorf("error = %+v", e)
	}
	if calls != 4 {
		t.Errorf("calls = %d, want 4", calls)
	}
	if e.ExitCode() != api.ExitRetryable {
		t.Errorf("ExitCode = %d, want %d", e.ExitCode(), api.ExitRetryable)
	}
	if !strings.HasPrefix(e.Error(), "systemone: 529 after 4 attempts") {
		t.Errorf("Error() = %q", e.Error())
	}
	// Three waits between four attempts, growing: 500ms, 1s, 2s (+ jitter ≤ half).
	if len(*waits) != 3 {
		t.Fatalf("waits = %v, want 3", *waits)
	}
	for i, base := range []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second} {
		if w := (*waits)[i]; w < base || w > base+base/2 {
			t.Errorf("wait %d = %s, want within [%s, %s]", i, w, base, base+base/2)
		}
	}
}

func TestTransientStatusesAndTransportAreRetried(t *testing.T) {
	for _, status := range []int{502, 503, 504} {
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if atomic.AddInt32(&calls, 1) < 3 {
				http.Error(w, "upstream down", status)
				return
			}
			io.WriteString(w, okAnswer())
		}))
		c, _ := newTestClient(t, srv)
		if _, err := c.Ask(context.Background(), "x", sampleQuestions()); err != nil {
			t.Errorf("%d: Ask: %v", status, err)
		}
		if calls != 3 {
			t.Errorf("%d: calls = %d, want 3", status, calls)
		}
		srv.Close()
	}

	// A closed server is a transport error: retried, then reported as retryable.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	c, _ := newTestClient(t, srv)
	c.MaxAttempts = 2
	srv.Close()
	_, err := c.Ask(context.Background(), "x", sampleQuestions())
	var e *Error
	if !errors.As(err, &e) || e.Status != 0 || !e.Retryable || e.Attempts != 2 || e.Err == nil {
		t.Fatalf("transport error = %+v (%v)", e, err)
	}
	if e.ExitCode() != api.ExitRetryable {
		t.Errorf("ExitCode = %d", e.ExitCode())
	}
	if !strings.Contains(e.Error(), "after 2 attempts") {
		t.Errorf("Error() = %q", e.Error())
	}
}

func TestUnprocessableEntityFields(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		io.WriteString(w, `{"detail":[{"loc":["body","questions","label","criteria"],"msg":"too many options","type":"value_error"}]}`)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv)

	_, err := c.Ask(context.Background(), "x", sampleQuestions())
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %T %v", err, err)
	}
	if e.Status != 422 || e.Retryable || len(e.Fields) != 1 || e.Fields[0].Msg != "too many options" || e.Fields[0].Type != "value_error" {
		t.Errorf("error = %+v", e)
	}
	if e.ExitCode() != api.ExitInvalid {
		t.Errorf("ExitCode = %d, want %d", e.ExitCode(), api.ExitInvalid)
	}
	if got := e.Error(); got != "systemone: 422 Unprocessable Entity: questions.label.criteria: too many options" {
		t.Errorf("Error() = %q", got)
	}
	if calls != 1 {
		t.Errorf("422 was retried: calls = %d", calls)
	}
}

func TestBadRequestDetailStringOrObject(t *testing.T) {
	bodies := map[string]string{
		`{"detail":"state is required"}`:                 "systemone: 400 Bad Request: state is required",
		`{"detail": {"error": "bad state", "code": 12}}`: `systemone: 400 Bad Request: {"error":"bad state","code":12}`,
		"<html>nope</html>\nmore":                        "systemone: 400 Bad Request: <html>nope</html>",
	}
	for body, want := range bodies {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, body)
		}))
		c, _ := newTestClient(t, srv)
		_, err := c.Ask(context.Background(), "x", sampleQuestions())
		srv.Close()
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("err = %T %v", err, err)
		}
		if e.Error() != want || e.ExitCode() != api.ExitInvalid {
			t.Errorf("body %s: Error() = %q (exit %d), want %q", body, e.Error(), e.ExitCode(), want)
		}
	}
}

func TestUnauthorizedIsNotRetried(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"detail":"invalid api key"}`)
	}))
	defer srv.Close()
	c, waits := newTestClient(t, srv)

	_, err := c.Ask(context.Background(), "x", sampleQuestions())
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %T %v", err, err)
	}
	if e.Status != 401 || e.Retryable || e.Attempts != 1 || e.Detail != "invalid api key" {
		t.Errorf("error = %+v", e)
	}
	if e.ExitCode() != api.ExitAuth {
		t.Errorf("ExitCode = %d, want %d", e.ExitCode(), api.ExitAuth)
	}
	if calls != 1 || len(*waits) != 0 {
		t.Errorf("calls = %d, waits = %v; want one call and no backoff", calls, *waits)
	}
	if got := (&Error{Status: 403}).ExitCode(); got != api.ExitAuth {
		t.Errorf("403 ExitCode = %d", got)
	}
	if got := (&Error{Status: 413}).ExitCode(); got != api.ExitInvalid {
		t.Errorf("413 ExitCode = %d", got)
	}
	if got := (&Error{Status: 500}).ExitCode(); got != api.ExitFailed {
		t.Errorf("500 ExitCode = %d", got)
	}
}

func TestContextCancelledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(529)
	}))
	defer srv.Close()

	// Real sleep (no hook): the cancel during the first backoff must cut it short.
	c := New(srv.URL, testToken, "", 5*time.Second)
	c.sleep = nil
	done := make(chan error, 1)
	go func() {
		_, err := c.Ask(ctx, "x", sampleQuestions())
		done <- err
	}()
	for atomic.LoadInt32(&calls) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(400 * time.Millisecond): // the first backoff alone is ≥ 500ms
		t.Fatal("Ask did not return promptly after cancel")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestClientSideValidation(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		io.WriteString(w, okAnswer())
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv)

	tooMany := make(map[string]string, 256)
	for i := 0; i < 256; i++ {
		tooMany["opt"+string(rune('A'+i%26))+string(rune('0'+i/26))] = "x"
	}
	if len(tooMany) != 256 {
		t.Fatalf("test setup: %d options", len(tooMany))
	}
	cases := map[string]map[string]Question{
		"no questions":          {},
		"choice with 0 options": {"q": Choice("pick", map[string]string{})},
		"choice with nil":       {"q": Choice("pick", nil)},
		"choice with 256":       {"q": Choice("pick", tooMany)},
		"noul without text":     {"q": Noul("  ")},
	}
	for name, qs := range cases {
		_, err := c.Ask(context.Background(), "x", qs)
		var e *Error
		if !errors.As(err, &e) {
			t.Errorf("%s: err = %T %v, want *Error", name, err, err)
			continue
		}
		if e.Status != 0 || e.Retryable || e.ExitCode() != api.ExitInvalid {
			t.Errorf("%s: error = %+v (exit %d)", name, e, e.ExitCode())
		}
		if !strings.HasPrefix(e.Error(), "systemone: ") {
			t.Errorf("%s: Error() = %q", name, e.Error())
		}
	}
	if calls != 0 {
		t.Errorf("invalid requests reached the server: %d calls", calls)
	}

	// 255 options is the limit, and allowed.
	delete(tooMany, "optA0")
	if _, err := c.Ask(context.Background(), "x", map[string]Question{"q": Choice("pick", tooMany)}); err != nil {
		t.Errorf("255 options: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestInvalidSuccessBody(t *testing.T) {
	for _, body := range []string{"not json", `"a string"`, "null"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, body)
		}))
		c, _ := newTestClient(t, srv)
		_, err := c.Ask(context.Background(), "x", sampleQuestions())
		srv.Close()
		var e *Error
		if !errors.As(err, &e) || e.Status != 200 || e.Detail != "invalid response" || e.Retryable {
			t.Errorf("body %q: err = %+v (%v)", body, e, err)
		}
	}
}

func TestDebugMasksTokenAndRedactsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okAnswer())
	}))
	defer srv.Close()
	var buf bytes.Buffer
	c, _ := newTestClient(t, srv)
	c.Debug = &buf
	c.Redact = func(b []byte) []byte {
		return bytes.ReplaceAll(b, []byte("TOP-SECRET"), []byte("[redacted]"))
	}

	if _, err := c.Ask(context.Background(), "ticket says TOP-SECRET", sampleQuestions()); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, testToken) {
		t.Errorf("debug leaks the token:\n%s", out)
	}
	if !strings.Contains(out, MaskToken(testToken)) {
		t.Errorf("debug lacks the masked token %q:\n%s", MaskToken(testToken), out)
	}
	if strings.Contains(out, "TOP-SECRET") || !strings.Contains(out, "[redacted]") {
		t.Errorf("Redact was not applied to the debug body:\n%s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "> POST "+srv.URL) || !strings.HasPrefix(lines[1], "< 200") {
		t.Errorf("debug lines = %q", lines)
	}
}

func TestDefaultsAndHelpers(t *testing.T) {
	c := New("", "k", "", time.Second)
	if c.Endpoint != DefaultEndpoint || c.Model != DefaultModel || c.ModelName() != DefaultModel {
		t.Errorf("defaults: %+v", c)
	}
	if c.Host() != "api.typesafe.ai" {
		t.Errorf("Host() = %q", c.Host())
	}
	if c.HTTP == nil || c.HTTP.Timeout != time.Second {
		t.Errorf("HTTP client: %+v", c.HTTP)
	}
	if (&Client{}).ModelName() != DefaultModel {
		t.Errorf("zero client ModelName = %q", (&Client{}).ModelName())
	}
	if got := MaskToken("sk-typesafe-0123456789abcdef"); got != "sk-t********************cdef" {
		t.Errorf("MaskToken = %q", got)
	}
	if got := MaskToken("short"); got != "*****" {
		t.Errorf("MaskToken(short) = %q", got)
	}

	// Retry-After is honoured in seconds and as a date, and capped at 30s.
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	for in, want := range map[string]time.Duration{
		"3":   3 * time.Second,
		"120": 30 * time.Second,
		"-1":  0,
		now.Add(5 * time.Second).UTC().Format(http.TimeFormat): 5 * time.Second,
	} {
		if d, ok := c.parseRetryAfter(in); !ok || d != want {
			t.Errorf("parseRetryAfter(%q) = %s, %v; want %s", in, d, ok, want)
		}
	}
	if _, ok := c.parseRetryAfter("soon"); ok {
		t.Errorf("parseRetryAfter(soon) should not parse")
	}
}
