package orbtr

// The hand-written layer: configuration, retries and Retry-After, timeouts, error bodies, the event-stream
// decoder, the string-or-array union and the flow helpers.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type reply struct {
	status int
	header map[string]string
	body   string
	err    error
}

type scripted struct {
	replies []reply
	sent    []*http.Request
	bodies  [][]byte
}

func (s *scripted) RoundTrip(req *http.Request) (*http.Response, error) {
	var b []byte
	if req.Body != nil {
		b, _ = io.ReadAll(req.Body)
	}
	s.sent = append(s.sent, req)
	s.bodies = append(s.bodies, b)
	r := s.replies[0]
	s.replies = s.replies[1:]
	if r.err != nil {
		return nil, r.err
	}
	h := http.Header{}
	for k, v := range r.header {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: r.status, Header: h, Body: io.NopCloser(strings.NewReader(r.body)), Request: req}, nil
}

var okList = reply{status: 200, body: `{"object":"list","data":[]}`}

func scriptedClient(t *testing.T, opts []ClientOption, replies ...reply) (*Client, *scripted, *[]time.Duration) {
	t.Helper()
	tr := &scripted{replies: replies}
	opts = append([]ClientOption{WithAPIKey("k"), WithHTTPClient(&http.Client{Transport: tr})}, opts...)
	c, err := NewClient(opts...)
	if err != nil {
		t.Fatal(err)
	}
	waits := &[]time.Duration{}
	c.sleep = func(_ context.Context, d time.Duration) error { *waits = append(*waits, d); return nil }
	return c, tr, waits
}

func TestKeyFromEnvironmentAsBearer(t *testing.T) {
	t.Setenv("ORBTR_API_KEY", "env-key")
	t.Setenv("ORBTR_BASE_URL", "")
	tr := &scripted{replies: []reply{okList}}
	c, err := NewClient(WithHTTPClient(&http.Client{Transport: tr}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Models.List(ctx); err != nil {
		t.Fatal(err)
	}
	if tr.sent[0].Header.Get("Authorization") != "Bearer env-key" || tr.sent[0].URL.String() != "https://api.orbtr.ai/v1/models" {
		t.Errorf("sent %s %v", tr.sent[0].URL, tr.sent[0].Header)
	}
}

func TestNoKeyIsRefused(t *testing.T) {
	t.Setenv("ORBTR_API_KEY", "")
	if _, err := NewClient(); err == nil {
		t.Fatal("a client with no key was made")
	}
}

func TestBaseURLFromOptionThenEnvironment(t *testing.T) {
	c, tr, _ := scriptedClient(t, []ClientOption{WithBaseURL("http://127.0.0.1:9/")}, okList)
	_, _ = c.Models.List(ctx)
	if tr.sent[0].URL.String() != "http://127.0.0.1:9/v1/models" {
		t.Errorf("url %s", tr.sent[0].URL)
	}
	t.Setenv("ORBTR_BASE_URL", "http://127.0.0.2:9")
	c, tr, _ = scriptedClient(t, nil, okList)
	_, _ = c.Models.List(ctx)
	if tr.sent[0].URL.String() != "http://127.0.0.2:9/v1/models" {
		t.Errorf("url %s", tr.sent[0].URL)
	}
}

func TestACallersHeaderCannotReplaceAuthorization(t *testing.T) {
	c, tr, _ := scriptedClient(t, []ClientOption{WithHeader("Authorization", "Bearer other")}, okList)
	_, _ = c.Models.List(ctx, WithRequestHeader("Authorization", "Bearer x"), WithRequestHeader("X-Trace", "1"))
	if got := tr.sent[0].Header.Values("Authorization"); !reflect.DeepEqual(got, []string{"Bearer k"}) || tr.sent[0].Header.Get("X-Trace") != "1" {
		t.Errorf("headers %v", tr.sent[0].Header)
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	c, tr, waits := scriptedClient(t, nil, reply{status: 429, header: map[string]string{"Retry-After": "3"}, body: `{"error":"api_key_rate_limit_exceeded"}`}, okList)
	if _, err := c.Models.List(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tr.sent) != 2 || !reflect.DeepEqual(*waits, []time.Duration{3 * time.Second}) {
		t.Errorf("sent %d waits %v", len(tr.sent), *waits)
	}
}

func TestALongRetryAfterIsReturned(t *testing.T) {
	body := `{"type":"error","error":{"type":"no_servable_placement","message":"nothing can serve the model now"}}`
	c, tr, _ := scriptedClient(t, nil, reply{status: 503, header: map[string]string{"Retry-After": "3600"}, body: body})
	_, err := c.Models.List(ctx)
	var apiErr *APIError
	if !errors.Is(err, ErrInternalServer) || !errors.As(err, &apiErr) || apiErr.Code != "no_servable_placement" || apiErr.Header.Get("Retry-After") != "3600" {
		t.Fatalf("err %v", err)
	}
	if len(tr.sent) != 1 {
		t.Errorf("sent %d", len(tr.sent))
	}
}

func TestServerErrorsRetriedUpToMaxRetries(t *testing.T) {
	fail := reply{status: 529, body: `{"type":"error","error":{"type":"fleet_overload","message":"busy"}}`}
	c, tr, waits := scriptedClient(t, []ClientOption{WithMaxRetries(2)}, fail, fail, fail)
	if _, err := c.Models.List(ctx); !errors.Is(err, ErrInternalServer) {
		t.Fatalf("err %v", err)
	}
	if len(tr.sent) != 3 || len(*waits) != 2 || (*waits)[1] < 750*time.Millisecond || (*waits)[1] > time.Second {
		t.Errorf("sent %d waits %v", len(tr.sent), *waits)
	}
}

func TestACallsRetriesOverrideTheClients(t *testing.T) {
	c, tr, _ := scriptedClient(t, []ClientOption{WithMaxRetries(5)}, reply{status: 500, body: "{}"}, okList)
	if _, err := c.Models.List(ctx, WithRequestMaxRetries(0)); !errors.Is(err, ErrInternalServer) {
		t.Fatalf("err %v", err)
	}
	if len(tr.sent) != 1 {
		t.Errorf("sent %d", len(tr.sent))
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	for status, target := range map[int]error{400: ErrBadRequest, 402: ErrPaymentRequired, 403: ErrPermissionDenied, 409: ErrConflict} {
		c, tr, _ := scriptedClient(t, nil, reply{status: status, body: `{"type":"error","error":{"type":"x","message":"no"}}`})
		if _, err := c.Models.List(ctx); !errors.Is(err, target) {
			t.Errorf("%d: err %v", status, err)
		}
		if len(tr.sent) != 1 {
			t.Errorf("%d: sent %d", status, len(tr.sent))
		}
	}
}

func TestConnectionFailureRetriedThenReturned(t *testing.T) {
	refused := errors.New("connection refused")
	c, tr, _ := scriptedClient(t, []ClientOption{WithMaxRetries(1)}, reply{err: refused}, reply{err: refused})
	_, err := c.Models.List(ctx)
	var ce *ConnectionError
	if !errors.As(err, &ce) || ce.Timeout || !errors.Is(err, refused) {
		t.Fatalf("err %v", err)
	}
	if len(tr.sent) != 2 {
		t.Errorf("sent %d", len(tr.sent))
	}
}

type hang struct{}

func (hang) RoundTrip(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, req.Context().Err()
}

func TestATimeoutIsAConnectionErrorWithTimeout(t *testing.T) {
	c, err := NewClient(WithAPIKey("k"), WithHTTPClient(&http.Client{Transport: hang{}}), WithTimeout(50*time.Millisecond), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Models.List(ctx)
	var ce *ConnectionError
	if !errors.As(err, &ce) || !ce.Timeout {
		t.Fatalf("err %v", err)
	}
}

func TestEveryRetryOfABatchCreateSendsTheSameKey(t *testing.T) {
	c, tr, _ := scriptedClient(t, nil, reply{status: 503, header: map[string]string{"Retry-After": "0"}, body: "{}"}, reply{status: 200, body: `{"id":"b"}`})
	if _, err := c.Messages.Batches.Create(ctx, MessageBatchCreateParams{Requests: []MessageBatchRequest{}}); err != nil {
		t.Fatal(err)
	}
	k0, k1 := tr.sent[0].Header.Get("Idempotency-Key"), tr.sent[1].Header.Get("Idempotency-Key")
	if k0 == "" || k0 != k1 {
		t.Errorf("keys %q %q", k0, k1)
	}
}

func TestNoOtherCallSendsAnIdempotencyKey(t *testing.T) {
	c, tr, _ := scriptedClient(t, nil, reply{status: 200, body: `{"input_tokens":1}`})
	if _, err := c.Messages.CountTokens(ctx, MessageCountTokensParams{Model: "m", Messages: []MessageParam{}}); err != nil {
		t.Fatal(err)
	}
	if tr.sent[0].Header.Get("Idempotency-Key") != "" {
		t.Error("count_tokens sent an Idempotency-Key")
	}
}

func TestOptionalParamsLeftUnsetAreNotSent(t *testing.T) {
	c, tr, _ := scriptedClient(t, nil, reply{status: 200, body: `{"input_tokens":1}`})
	_, _ = c.Messages.CountTokens(ctx, MessageCountTokensParams{Model: "m", Messages: []MessageParam{UserMessage("x")}})
	if string(tr.bodies[0]) != `{"model":"m","messages":[{"role":"user","content":"x"}]}` {
		t.Errorf("body %s", tr.bodies[0])
	}
	c, tr, _ = scriptedClient(t, nil, reply{status: 200, body: `{}`})
	_, _ = c.Messages.Create(ctx, MessageCreateParams{Model: "m", Messages: []MessageParam{MessageOfBlocks("user", ContentBlockParam{Type: "text", Text: String("x")})}, Temperature: Float(0), System: SystemPrompt("be brief")})
	if string(tr.bodies[0]) != `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"x"}]}],"system":"be brief","temperature":0}` {
		t.Errorf("body %s", tr.bodies[0])
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Duration{"2": 2 * time.Second, "0.5": 500 * time.Millisecond, "Wed, 25 Nov 2026 10:00:30 GMT": 30 * time.Second, "Wed, 25 Nov 2026 09:00:00 GMT": 0} {
		if got, ok := ParseRetryAfter(in, now); !ok || got != want {
			t.Errorf("%q: %v %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "soon"} {
		if _, ok := ParseRetryAfter(in, now); ok {
			t.Errorf("%q read as a wait", in)
		}
	}
	if MaxRetryAfter != time.Minute {
		t.Errorf("MaxRetryAfter %v", MaxRetryAfter)
	}
}

func TestBackoff(t *testing.T) {
	if Backoff(0, 0) != 500*time.Millisecond || Backoff(1, 0) != time.Second || Backoff(10, 0) != 8*time.Second || Backoff(0, 1) != 375*time.Millisecond {
		t.Error("backoff")
	}
	if !IsRetryableStatus(429) || !IsRetryableStatus(500) || !IsRetryableStatus(529) || IsRetryableStatus(400) || IsRetryableStatus(404) || IsRetryableStatus(409) {
		t.Error("retryable statuses")
	}
}

func TestDescribeErrorBodies(t *testing.T) {
	for body, want := range map[string][3]string{
		`{"type":"error","error":{"type":"insufficient_ot","message":"short"}}`:                         {"insufficient_ot", "short", ""},
		`{"error":{"message":"m","type":"authentication_error","code":null,"param":null}}`:              {"authentication_error", "m", ""},
		`{"error":"key_expired_or_revoked","reason":"rotated"}`:                                         {"key_expired_or_revoked", "", "rotated"},
		`{"type":"error","error":"assistant_mismatch","stored":"a","requested":"b","message":"pinned"}`: {"assistant_mismatch", "pinned", ""},
		`text`: {"", "", ""},
	} {
		c, m, r := describeErrorBody([]byte(body))
		if [3]string{c, m, r} != want {
			t.Errorf("%s: %q %q %q", body, c, m, r)
		}
	}
}

func TestARevokedKeysReason(t *testing.T) {
	c, _, _ := scriptedClient(t, nil, reply{status: 401, body: `{"error":"key_expired_or_revoked","reason":"revoked"}`})
	_, err := c.Models.List(ctx)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Reason != "revoked" || !errors.Is(err, ErrAuthentication) {
		t.Fatalf("err %v", err)
	}
}

func decodeAll(t *testing.T, text string, size int) [][2]string {
	t.Helper()
	d := NewSSEDecoder(&chunkedReader{data: []byte(text), n: size})
	var out [][2]string
	for {
		ev, err := d.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, [2]string{ev.Event, string(ev.Data)})
	}
}

func TestSSEDecoder(t *testing.T) {
	text := ": comment\r\nevent: a\r\ndata: 1\r\ndata: 2\r\n\r\nevent: b\rdata:x\r\rdata: tail"
	for _, size := range []int{1, 2, 3, 5, 100} {
		got := decodeAll(t, text, size)
		if want := [][2]string{{"a", "1\n2"}, {"b", "x"}, {"", "tail"}}; !reflect.DeepEqual(got, want) {
			t.Errorf("size %d: %v", size, got)
		}
	}
	if got := decodeAll(t, "data: café ☕\n\n", 1); got[0][1] != "café ☕" {
		t.Errorf("multibyte %v", got)
	}
}

func sseClient(t *testing.T, body string) *Client {
	t.Helper()
	c, _, _ := scriptedClient(t, nil, reply{status: 200, header: map[string]string{"Content-Type": "text/event-stream"}, body: body})
	return c
}

const startEvent = `event: message_start
data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}

`

func TestAnErrorEventEndsTheStream(t *testing.T) {
	body := startEvent + "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"inference stream failed\"}}\n\n"
	s, err := sseClient(t, body).Messages.CreateStreaming(ctx, MessageCreateParams{Model: "x", Messages: []MessageParam{}})
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for s.Next() {
		seen = append(seen, s.Current().Type)
	}
	var se *StreamError
	if !errors.As(s.Err(), &se) || se.Code != "api_error" || !reflect.DeepEqual(seen, []string{"message_start"}) {
		t.Fatalf("err %v seen %v", s.Err(), seen)
	}
}

func TestProgressEventsPassThrough(t *testing.T) {
	body := "event: orbtr.ai.turn.started\ndata: {\"turn\":\"t1\"}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	s, err := sseClient(t, body).Messages.CreateStreaming(ctx, MessageCreateParams{Model: "x", Messages: []MessageParam{}})
	if err != nil {
		t.Fatal(err)
	}
	var evs []MessageStreamEvent
	for s.Next() {
		evs = append(evs, s.Current())
	}
	if len(evs) != 2 || evs[0].Type != "orbtr.ai.turn.started" || evs[0].RawJSON() != `{"turn":"t1"}` || evs[1].MessageStop == nil {
		t.Fatalf("events %+v", evs)
	}
}

func TestToolInputGatheredFromJSONPieces(t *testing.T) {
	body := strings.Join([]string{
		strings.TrimSpace(startEvent),
		`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"lookup","input":{}}}`,
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\": \"Sy"}}`,
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"dney\"}"}}`,
		`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
		`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":5}}`,
		`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
	}, "\n\n") + "\n\n"
	s, err := sseClient(t, body).Messages.CreateStreaming(ctx, MessageCreateParams{Model: "x", Messages: []MessageParam{}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.FinalMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Content[0].Input, map[string]any{"city": "Sydney"}) || *m.StopReason != "tool_use" || m.Usage.OutputTokens != 5 || m.Usage.InputTokens != 1 {
		t.Errorf("message %+v", m)
	}
}

func TestStringOr(t *testing.T) {
	for _, tc := range []struct {
		v    StringOr[string]
		want string
	}{{StringOf[string]("a"), `"a"`}, {ArrayOf("a", "b"), `["a","b"]`}, {ArrayOf[string](), `[]`}} {
		b, err := json.Marshal(tc.v)
		if err != nil || string(b) != tc.want {
			t.Errorf("%s %v", b, err)
		}
		var back StringOr[string]
		if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back, tc.v) {
			t.Errorf("round trip %s: %+v %v", b, back, err)
		}
	}
}

func TestFlowModels(t *testing.T) {
	if m, err := FlowModel("partition:p1:flow:council", 0); err != nil || m != "partition:p1:flow:council" {
		t.Error(m, err)
	}
	if m := MustFlowModel("partition:p1:flow:council", 3); m != "partition:p1:flow:council@3" {
		t.Error(m)
	}
	for _, bad := range []struct {
		id string
		v  int
	}{{"orbtr-model-1", 0}, {"partition:p1:flow:council@2", 3}, {"partition:p1:flow:council", -1}} {
		if _, err := FlowModel(bad.id, bad.v); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if id, v, ok := ParseFlowModel("partition:p1:flow:council@2"); !ok || id != "partition:p1:flow:council" || v != 2 {
		t.Error(id, v, ok)
	}
	if _, _, ok := ParseFlowModel("orbtr-model-1"); ok || !IsFlowModel("partition:p1:flow:x") {
		t.Error("flow detection")
	}
}

func TestUnknownResponseFieldsAreKept(t *testing.T) {
	var m Model
	if err := json.Unmarshal([]byte(`{"id":"a","object":"model","created":0,"owned_by":"orbtr","orbtr":{"kind":"flow"}}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.ID != "a" || !strings.Contains(m.RawJSON(), `"orbtr":{"kind":"flow"}`) {
		t.Errorf("model %+v %s", m, m.RawJSON())
	}
}
