package orbtr

// The client over real HTTP against a local fake of the /v1 surface: every call, streaming, flows as models,
// retries after 429 and 503, typed errors, and each example in examples/.
//
// The fake answers each route with the body recorded from its handler (fixtures/), after the checks
// api.orbtr.ai makes that a client can get wrong: the bearer key, the JSON body, the Idempotency-Key a batch
// create needs and refuses elsewhere, and a flow id the key cannot read. The X-Fake-Scenario header scripts
// failures: "retry-429" and "retry-503" fail the first call of a scenario id ("<kind>:<id>") with
// Retry-After: 0, and "stream-error" ends a stream with an error event.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
)

const fakeKey = "test-key"

type fakeAPI struct {
	t        *testing.T
	mu       sync.Mutex
	requests []*http.Request
	failed   map[string]bool
}

func (f *fakeAPI) fixture(w http.ResponseWriter, name string) {
	fx := loadFixture(f.t, name)
	for k, v := range fx.Response.Headers {
		w.Header().Set(k, v)
	}
	w.WriteHeader(fx.Response.Status)
	_, _ = io.WriteString(w, fx.Response.Body)
}

func writeJSON(w http.ResponseWriter, status int, body any, header map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	for k, v := range header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func apiErrorBody(code, msg string) map[string]any {
	return map[string]any{"type": "error", "error": map[string]string{"type": code, "message": msg}}
}

var batchPath = regexp.MustCompile(`^/v1/messages/batches/([^/]+)(/cancel|/results)?$`)

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, r.Clone(r.Context()))
	f.mu.Unlock()
	var body map[string]any
	if len(raw) > 0 && json.Unmarshal(raw, &body) != nil {
		writeJSON(w, 400, apiErrorBody("invalid_request_error", "request body is not valid JSON"), nil)
		return
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		f.fixture(w, "error-missing-bearer-token")
		return
	}
	if auth != "Bearer "+fakeKey {
		writeJSON(w, 401, map[string]string{"error": "invalid_api_key"}, nil)
		return
	}
	scenario := r.Header.Get("X-Fake-Scenario")
	kind, _, _ := strings.Cut(scenario, ":")
	f.mu.Lock()
	first := !f.failed[scenario]
	if kind == "retry-429" || kind == "retry-503" {
		f.failed[scenario] = true
	}
	f.mu.Unlock()
	if first && kind == "retry-429" {
		writeJSON(w, 429, apiErrorBody("partition_inflight_exceeded", "too many concurrent inference requests for this partition"), map[string]string{"Retry-After": "0"})
		return
	}
	if first && kind == "retry-503" {
		writeJSON(w, 503, apiErrorBody("no_servable_placement", "nothing can serve the model now"), map[string]string{"Retry-After": "0"})
		return
	}
	route := r.Method + " " + r.URL.Path
	idem := r.Header.Get("Idempotency-Key")
	if idem != "" && route != "POST /v1/messages/batches" && route != "POST /v1/messages/count_tokens" && !strings.HasSuffix(route, "/cancel") {
		writeJSON(w, 400, apiErrorBody("invalid_request_error", "this route takes no Idempotency-Key"), nil)
		return
	}
	model, _ := body["model"].(string)
	stream, _ := body["stream"].(bool)
	isFlow := IsFlowModel(model)
	if isFlow && strings.Split(model, "@")[0] != "partition:p1:flow:council" {
		f.fixture(w, "error-flow-not-found")
		return
	}
	switch route {
	case "GET /v1/models":
		f.fixture(w, "models-list")
		return
	case "POST /v1/messages":
		switch {
		case stream && kind == "stream-error":
			start := strings.SplitN(loadFixture(f.t, "messages-stream").Response.Body, "\n\n", 2)[0]
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, start+"\n\nevent: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"inference stream failed\"}}\n\n")
		case isFlow && stream:
			f.fixture(w, "messages-stream-flow")
		case isFlow:
			f.fixture(w, "messages-create-flow")
		case stream:
			f.fixture(w, "messages-stream")
		default:
			f.fixture(w, "messages-create")
		}
		return
	case "POST /v1/messages/count_tokens":
		f.fixture(w, "messages-count-tokens")
		return
	case "POST /v1/embeddings":
		f.fixture(w, "embeddings-create")
		return
	case "POST /v1/chat/completions":
		switch {
		case isFlow:
			f.fixture(w, "chat-completions-create-flow")
		case stream:
			f.fixture(w, "chat-completions-stream")
		default:
			f.fixture(w, "chat-completions-create")
		}
		return
	case "POST /v1/completions":
		f.fixture(w, "completions-create")
		return
	case "POST /v1/messages/batches":
		if idem == "" {
			writeJSON(w, 400, apiErrorBody("invalid_request_error", "Idempotency-Key is required"), nil)
			return
		}
		f.fixture(w, "batches-create")
		return
	case "GET /v1/messages/batches":
		f.fixture(w, "batches-list")
		return
	}
	if m := batchPath.FindStringSubmatch(r.URL.Path); m != nil {
		if m[1] != "msgbatch_sdk_01" {
			writeJSON(w, 404, apiErrorBody("not_found_error", "message batch not found"), nil)
			return
		}
		switch {
		case r.Method == http.MethodGet && m[2] == "":
			f.fixture(w, "batches-retrieve")
			return
		case r.Method == http.MethodPost && m[2] == "/cancel":
			f.fixture(w, "batches-cancel")
			return
		case r.Method == http.MethodGet && m[2] == "/results":
			f.fixture(w, "batches-results")
			return
		}
	}
	writeJSON(w, 404, apiErrorBody("not_found_error", "no route "+route), nil)
}

func (f *fakeAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func TestIntegrationAgainstTheFakeSurface(t *testing.T) {
	fake := &fakeAPI{t: t, failed: map[string]bool{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c, err := NewClient(WithAPIKey(fakeKey), WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	user := func(s string) []MessageParam { return []MessageParam{UserMessage(s)} }

	t.Run("a whole session", func(t *testing.T) {
		models, err := c.Models.List(ctx)
		must(err)
		if len(models.Data) != 3 {
			t.Errorf("models %d", len(models.Data))
		}
		msg, err := c.Messages.Create(ctx, MessageCreateParams{Model: "orbtr-model-1", MaxTokens: Int(256), Messages: user("sea")})
		must(err)
		if msg.Content[0].Text != "The sea is wide and grey." {
			t.Errorf("text %q", msg.Content[0].Text)
		}
		s, err := c.Messages.CreateStreaming(ctx, MessageCreateParams{Model: "orbtr-model-1", MaxTokens: Int(256), Messages: user("sea")})
		must(err)
		if text, err := s.FinalText(); err != nil || text != "The sea is wide and grey." {
			t.Errorf("stream %q %v", text, err)
		}
		n, err := c.Messages.CountTokens(ctx, MessageCountTokensParams{Model: "orbtr-model-1", Messages: user("how long?")})
		must(err)
		if n.InputTokens != 14 {
			t.Errorf("tokens %d", n.InputTokens)
		}
		flow, err := c.Messages.Create(ctx, MessageCreateParams{Model: MustFlowModel("partition:p1:flow:council", 2), MaxTokens: Int(64), Messages: user("review this")})
		must(err)
		if flow.Usage.Orbtr == nil || flow.Usage.Orbtr.Version != 2 {
			t.Errorf("flow usage %+v", flow.Usage)
		}
		if _, err := c.Messages.Create(ctx, MessageCreateParams{Model: MustFlowModel("partition:p1:flow:missing", 0), MaxTokens: Int(64), Messages: user("x")}); !errors.Is(err, ErrNotFound) {
			t.Errorf("missing flow: %v", err)
		}
		emb, err := c.Embeddings.Create(ctx, EmbeddingCreateParams{Model: "orbtr-embed-1", Input: ArrayOf("the sea", "the sky")})
		must(err)
		if len(emb.Data) != 2 {
			t.Errorf("embeddings %d", len(emb.Data))
		}
		chat, err := c.Chat.Completions.Create(ctx, ChatCompletionCreateParams{Model: "orbtr-model-1", Messages: []ChatMessageParam{ChatMessage("user", "hi")}})
		must(err)
		if *chat.Choices[0].Message.Content != "The sea is wide and grey." {
			t.Errorf("chat %q", *chat.Choices[0].Message.Content)
		}
		cs, err := c.Chat.Completions.CreateStreaming(ctx, ChatCompletionCreateParams{Model: "orbtr-model-1", Messages: []ChatMessageParam{ChatMessage("user", "hi")}})
		must(err)
		var streamed string
		for cs.Next() {
			streamed += cs.Current().Choices[0].Delta.Content
		}
		if cs.Err() != nil || streamed != "The sea is wide and grey." {
			t.Errorf("chat stream %q %v", streamed, cs.Err())
		}
		legacy, err := c.Completions.Create(ctx, CompletionCreateParams{Model: "orbtr-model-1", Prompt: StringOf[string]("The sea is")})
		must(err)
		if legacy.Choices[0].Text != "The sea is wide and grey." {
			t.Errorf("completion %q", legacy.Choices[0].Text)
		}
		batch, err := c.Messages.Batches.Create(ctx, MessageBatchCreateParams{Requests: []MessageBatchRequest{
			{CustomID: "first", Params: MessageCreateParams{Model: "orbtr-model-1", MaxTokens: Int(64), Messages: user("Say hello.")}},
		}})
		must(err)
		page, err := c.Messages.Batches.List(ctx, MessagesBatchesListParams{Limit: Int(20)})
		must(err)
		got, err := c.Messages.Batches.Get(ctx, batch.ID)
		must(err)
		canceled, err := c.Messages.Batches.Cancel(ctx, batch.ID)
		must(err)
		results, err := c.Messages.Batches.Results(ctx, batch.ID)
		must(err)
		if batch.ID != "msgbatch_sdk_01" || len(page.Data) != 1 || got.ProcessingStatus != "ended" || canceled.ProcessingStatus != "canceling" ||
			len(results) != 2 || results[0].Result.Type != "succeeded" || results[1].Result.Type != "errored" {
			t.Errorf("batches %+v %+v %+v %+v %+v", batch, page, got, canceled, results)
		}
		if _, err := c.Messages.Batches.Get(ctx, "msgbatch_missing"); !errors.Is(err, ErrNotFound) {
			t.Errorf("missing batch: %v", err)
		}
		fake.mu.Lock()
		for _, r := range fake.requests {
			if r.Header.Get("Authorization") != "Bearer "+fakeKey || r.Header.Get("X-Api-Key") != "" {
				t.Errorf("%s %s headers %v", r.Method, r.URL.Path, r.Header)
			}
		}
		fake.mu.Unlock()
	})

	t.Run("429 and 503 are retried after Retry-After, and a batch create keeps its key", func(t *testing.T) {
		n := fake.count()
		msg, err := c.Messages.Create(ctx, MessageCreateParams{Model: "orbtr-model-1", MaxTokens: Int(16), Messages: user("hi")}, WithRequestHeader("X-Fake-Scenario", "retry-429:a"))
		must(err)
		if msg.ID != "msg_sdk_01" || fake.count()-n != 2 {
			t.Errorf("message %s after %d requests", msg.ID, fake.count()-n)
		}
		n = fake.count()
		_, err = c.Messages.Batches.Create(ctx, MessageBatchCreateParams{Requests: []MessageBatchRequest{}}, WithRequestHeader("X-Fake-Scenario", "retry-503:b"))
		must(err)
		fake.mu.Lock()
		sent := fake.requests[n:]
		fake.mu.Unlock()
		if len(sent) != 2 || sent[0].Header.Get("Idempotency-Key") == "" || sent[0].Header.Get("Idempotency-Key") != sent[1].Header.Get("Idempotency-Key") {
			t.Errorf("batch create sent %d requests", len(sent))
		}
	})

	t.Run("a wrong key is not retried", func(t *testing.T) {
		wrong, err := NewClient(WithAPIKey("wrong"), WithBaseURL(srv.URL))
		must(err)
		n := fake.count()
		_, err = wrong.Models.List(ctx)
		var apiErr *APIError
		if !errors.Is(err, ErrAuthentication) || !errors.As(err, &apiErr) || apiErr.Code != "invalid_api_key" || fake.count()-n != 1 {
			t.Errorf("err %v after %d requests", err, fake.count()-n)
		}
	})

	t.Run("an error event ends a stream", func(t *testing.T) {
		s, err := c.Messages.CreateStreaming(ctx, MessageCreateParams{Model: "orbtr-model-1", MaxTokens: Int(16), Messages: user("hi")}, WithRequestHeader("X-Fake-Scenario", "stream-error"))
		must(err)
		_, err = s.FinalMessage()
		var se *StreamError
		if !errors.As(err, &se) || se.Code != "api_error" {
			t.Errorf("err %v", err)
		}
	})

	t.Run("every example runs against the fake", func(t *testing.T) {
		expected := map[string]string{
			"send-message":     `orbtr-model-1\n[\s\S]*The sea is wide and grey\.`,
			"stream-reply":     `The sea is wide and grey\.`,
			"count-tokens":     `input tokens: 14\ncontext length: 131072`,
			"chat-completions": `The sea is wide and grey\.`,
			"flow-model":       `the council agrees\nflow partition:p1:flow:council version 2: 7 OT over 2 steps`,
		}
		dirs, err := filepath.Glob(filepath.Join("examples", "*"))
		must(err)
		var names []string
		for _, d := range dirs {
			names = append(names, filepath.Base(d))
		}
		sort.Strings(names)
		var want []string
		for n := range expected {
			want = append(want, n)
		}
		sort.Strings(want)
		if strings.Join(names, ",") != strings.Join(want, ",") {
			t.Fatalf("examples %v, expected %v", names, want)
		}
		bin := t.TempDir()
		build := exec.Command("go", "build", "-o", bin+string(os.PathSeparator), "./examples/...")
		build.Env = append(os.Environ(), "GOWORK=off")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build examples: %v\n%s", err, out)
		}
		for _, name := range names {
			cmd := exec.Command(filepath.Join(bin, name))
			cmd.Env = append(os.Environ(), "ORBTR_API_KEY="+fakeKey, "ORBTR_BASE_URL="+srv.URL, "ORBTR_MODEL=orbtr-model-1",
				"ORBTR_FLOW_ID=partition:p1:flow:council", "ORBTR_FLOW_VERSION=2")
			out, err := cmd.Output()
			if err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			if !regexp.MustCompile(expected[name]).Match(out) {
				t.Errorf("%s printed %q", name, out)
			}
		}
	})
}
