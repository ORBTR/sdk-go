package orbtr

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	mrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// The client's defaults.
const (
	// DefaultBaseURL is the API origin.
	DefaultBaseURL = "https://api.orbtr.ai"
	// DefaultMaxRetries is how many times a failed call is retried.
	DefaultMaxRetries = 2
	// DefaultTimeout is how long a call may take.
	DefaultTimeout = 10 * time.Minute
	// MaxRetryAfter is the longest Retry-After a retry waits for; a longer one is returned to the caller.
	MaxRetryAfter = 60 * time.Second

	initialBackoff = 500 * time.Millisecond
	maxBackoff     = 8 * time.Second
)

// Client calls api.orbtr.ai. Its embedded resources (Messages, Models, Embeddings, Chat and Completions) hold
// one method per operation. It is safe for concurrent use.
type Client struct {
	resources

	apiKey     string
	baseURL    string
	maxRetries int
	timeout    time.Duration
	headers    http.Header
	httpClient *http.Client
	sleep      func(context.Context, time.Duration) error
}

// ClientOption configures NewClient.
type ClientOption func(*Client)

// WithAPIKey sets the API key, in place of the ORBTR_API_KEY environment variable.
func WithAPIKey(key string) ClientOption { return func(c *Client) { c.apiKey = key } }

// WithBaseURL sets the API origin, in place of ORBTR_BASE_URL and https://api.orbtr.ai.
func WithBaseURL(u string) ClientOption { return func(c *Client) { c.baseURL = u } }

// WithMaxRetries sets how many times a 429, a 5xx or a connection failure is retried.
func WithMaxRetries(n int) ClientOption { return func(c *Client) { c.maxRetries = n } }

// WithTimeout sets how long a call may take, retries apart; for a stream, how long its response headers may
// take.
func WithTimeout(d time.Duration) ClientOption { return func(c *Client) { c.timeout = d } }

// WithHeader adds a header to every call.
func WithHeader(key, value string) ClientOption { return func(c *Client) { c.headers.Add(key, value) } }

// WithHTTPClient sets the HTTP client calls are sent through.
func WithHTTPClient(h *http.Client) ClientOption { return func(c *Client) { c.httpClient = h } }

// NewClient makes a client. The API key comes from WithAPIKey or the ORBTR_API_KEY environment variable,
// and is sent as Authorization: Bearer <key>.
func NewClient(opts ...ClientOption) (*Client, error) {
	c := &Client{
		apiKey:     os.Getenv("ORBTR_API_KEY"),
		baseURL:    os.Getenv("ORBTR_BASE_URL"),
		maxRetries: DefaultMaxRetries,
		timeout:    DefaultTimeout,
		headers:    http.Header{},
		httpClient: http.DefaultClient,
		sleep:      sleepContext,
	}
	for _, o := range opts {
		o(c)
	}
	if c.apiKey == "" {
		return nil, errors.New("orbtr: no API key: pass WithAPIKey or set the ORBTR_API_KEY environment variable")
	}
	if c.baseURL == "" {
		c.baseURL = DefaultBaseURL
	}
	c.baseURL = strings.TrimRight(c.baseURL, "/")
	if _, err := url.Parse(c.baseURL); err != nil {
		return nil, fmt.Errorf("orbtr: base URL: %w", err)
	}
	c.resources = newResources(c)
	return c, nil
}

// RequestOption configures one call.
type RequestOption func(*requestConfig)

type requestConfig struct {
	headers        http.Header
	timeout        time.Duration
	maxRetries     int
	idempotencyKey string
}

// WithRequestHeader adds a header to this call.
func WithRequestHeader(key, value string) RequestOption {
	return func(r *requestConfig) { r.headers.Add(key, value) }
}

// WithRequestTimeout sets how long this call may take.
func WithRequestTimeout(d time.Duration) RequestOption {
	return func(r *requestConfig) { r.timeout = d }
}

// WithRequestMaxRetries sets this call's retries.
func WithRequestMaxRetries(n int) RequestOption {
	return func(r *requestConfig) { r.maxRetries = n }
}

// WithIdempotencyKey sets the Idempotency-Key of a call that takes one (creating a message batch). One is
// made when absent, and every retry of the call sends the same key.
func WithIdempotencyKey(key string) RequestOption {
	return func(r *requestConfig) { r.idempotencyKey = key }
}

// request is one call as a generated method describes it.
type request struct {
	method     string
	path       string
	query      url.Values
	body       any
	idempotent bool
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// IsRetryableStatus reports whether a response status is retried: 429 and every 5xx.
func IsRetryableStatus(status int) bool { return status == 429 || status >= 500 }

// Backoff is the wait before retry number attempt (0 for the first retry): exponential from 0.5 s, capped at
// 8 s, with up to a quarter taken off at random (random is in [0, 1)) so clients refused together do not
// retry together.
func Backoff(attempt int, random float64) time.Duration {
	d := float64(initialBackoff) * math.Pow(2, float64(attempt))
	if d > float64(maxBackoff) {
		d = float64(maxBackoff)
	}
	return time.Duration(d * (1 - 0.25*random))
}

// ParseRetryAfter reads Retry-After as seconds or an HTTP date; ok is false when it is absent or unreadable.
func ParseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	v := strings.TrimSpace(value)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if secs < 0 {
			secs = 0
		}
		return time.Duration(secs * float64(time.Second)), true
	}
	at, err := http.ParseTime(v)
	if err != nil {
		return 0, false
	}
	if d := at.Sub(now); d > 0 {
		return d, true
	}
	return 0, true
}

func newIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("orbtr-sdk-%d", time.Now().UnixNano())
	}
	return "orbtr-sdk-" + hex.EncodeToString(b[:])
}

// send sends a call, retrying a 429, a 5xx or a connection failure up to the call's retries. A Retry-After
// header sets the wait when it is at most MaxRetryAfter; a longer one ends the retries. The returned cancel
// ends the call's timeout and must be called once the body is read.
func (c *Client) send(ctx context.Context, req request, opts []RequestOption, accept string, streaming bool) (*http.Response, context.CancelFunc, error) {
	cfg := requestConfig{headers: http.Header{}, timeout: c.timeout, maxRetries: c.maxRetries}
	for _, o := range opts {
		o(&cfg)
	}
	var body []byte
	if req.body != nil {
		b, err := json.Marshal(req.body)
		if err != nil {
			return nil, nil, fmt.Errorf("orbtr: encode request: %w", err)
		}
		body = b
	}
	if req.idempotent && cfg.idempotencyKey == "" {
		cfg.idempotencyKey = newIdempotencyKey()
	}
	u := c.baseURL + req.path
	if len(req.query) > 0 {
		u += "?" + req.query.Encode()
	}
	for attempt := 0; ; attempt++ {
		callCtx, cancelCall := context.WithCancel(ctx)
		var expired atomic.Bool
		timer := time.AfterFunc(cfg.timeout, func() {
			expired.Store(true)
			cancelCall()
		})
		cancel := func() {
			timer.Stop()
			cancelCall()
		}
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		hr, err := http.NewRequestWithContext(callCtx, req.method, u, rdr)
		if err != nil {
			cancel()
			return nil, nil, fmt.Errorf("orbtr: build request: %w", err)
		}
		for k, vs := range c.headers {
			hr.Header[k] = append([]string(nil), vs...)
		}
		for k, vs := range cfg.headers {
			hr.Header[k] = append([]string(nil), vs...)
		}
		hr.Header.Set("Accept", accept)
		hr.Header.Set("User-Agent", "orbtr-sdk-go/"+Version)
		hr.Header.Set("Authorization", "Bearer "+c.apiKey)
		if body != nil {
			hr.Header.Set("Content-Type", "application/json")
		}
		if cfg.idempotencyKey != "" {
			hr.Header.Set("Idempotency-Key", cfg.idempotencyKey)
		}
		resp, err := c.httpClient.Do(hr)
		if err != nil {
			timedOut := expired.Load()
			cancel()
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				timedOut = true
			}
			if attempt < cfg.maxRetries {
				if err := c.sleep(ctx, Backoff(attempt, mrand.Float64())); err != nil {
					return nil, nil, err
				}
				continue
			}
			return nil, nil, &ConnectionError{Timeout: timedOut, Err: err}
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if streaming {
				// A stream's timeout covers its response headers; the caller's context and Close end it after.
				timer.Stop()
			}
			return resp, cancel, nil
		}
		if IsRetryableStatus(resp.StatusCode) && attempt < cfg.maxRetries {
			wait, ok := ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
			if !ok {
				wait = Backoff(attempt, mrand.Float64())
			}
			if wait <= MaxRetryAfter {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
				resp.Body.Close()
				cancel()
				if err := c.sleep(ctx, wait); err != nil {
					return nil, nil, err
				}
				continue
			}
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		cancel()
		return nil, nil, newAPIError(resp.StatusCode, data, resp.Header)
	}
}

func unmarshal(data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("orbtr: decode response: %w", err)
	}
	return nil
}

func (c *Client) doJSON(ctx context.Context, req request, out any, opts []RequestOption) error {
	resp, cancel, err := c.send(ctx, req, opts, "application/json", false)
	if err != nil {
		return err
	}
	defer cancel()
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Err: err}
	}
	return unmarshal(data, out)
}

func (c *Client) doNDJSON(ctx context.Context, req request, each func([]byte) error, opts []RequestOption) error {
	resp, cancel, err := c.send(ctx, req, opts, "application/x-ndjson", false)
	if err != nil {
		return err
	}
	defer cancel()
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionError{Err: err}
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if err := each(line); err != nil {
			return err
		}
	}
	return nil
}
