package orbtr

// Each call against the exchange recorded from api.orbtr.ai's handler: the client sends the recorded request
// (method, path, query, body) with the API key as a bearer token, and reads the recorded response.

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

var sea = []MessageParam{UserMessage("Write one sentence about the sea.")}

func TestEveryRecordedFixtureIsUsed(t *testing.T) {
	used := []string{
		"batches-cancel", "batches-create", "batches-list", "batches-results", "batches-retrieve",
		"chat-completions-create", "chat-completions-create-flow", "chat-completions-stream", "completions-create",
		"embeddings-create", "error-chat-no-key", "error-flow-not-found", "error-messages-no-key",
		"error-missing-bearer-token", "error-rate-limited", "messages-count-tokens", "messages-create",
		"messages-create-flow", "messages-stream", "messages-stream-flow", "messages-stream-rendered", "models-list",
	}
	if got := fixtureNames(t); !reflect.DeepEqual(got, used) {
		t.Fatalf("fixtures %v, tests use %v", got, used)
	}
}

func TestMessagesCreate(t *testing.T) {
	c, tr := fixtureClient(t, 0, "messages-create")
	msg, err := c.Messages.Create(ctx, MessageCreateParams{Model: "orbtr-model-1", MaxTokens: Int(256), Messages: sea})
	if err != nil {
		t.Fatal(err)
	}
	assertSent(t, tr.sent[0], "messages-create")
	if tr.sent[0].header.Get("Idempotency-Key") != "" {
		t.Error("messages.create sent an Idempotency-Key")
	}
	if msg.ID != "msg_sdk_01" || msg.Content[0].Type != "text" || msg.Content[0].Text != "The sea is wide and grey." {
		t.Fatalf("message %+v", msg)
	}
	if msg.StopReason == nil || *msg.StopReason != "end_turn" || msg.StopSequence != nil {
		t.Errorf("stop %v %v", msg.StopReason, msg.StopSequence)
	}
	if msg.Usage.InputTokens != 14 || msg.Usage.OutputTokens != 9 || msg.Usage.OTConsumed != 12 || msg.Usage.ReservationID != "res_sdk_1" {
		t.Errorf("usage %+v", msg.Usage)
	}
	if !strings.Contains(msg.RawJSON(), `"billing_profile":"bp_1"`) {
		t.Errorf("RawJSON %s", msg.RawJSON())
	}
}

func TestMessagesCreateStreamingNative(t *testing.T) {
	c, tr := fixtureClient(t, 0, "messages-stream")
	s, err := c.Messages.CreateStreaming(ctx, MessageCreateParams{Model: "orbtr-model-1", MaxTokens: Int(256), Messages: sea})
	if err != nil {
		t.Fatal(err)
	}
	assertSent(t, tr.sent[0], "messages-stream")
	if tr.sent[0].header.Get("Accept") != "text/event-stream" {
		t.Errorf("Accept %q", tr.sent[0].header.Get("Accept"))
	}
	var types []string
	var text string
	for s.Next() {
		ev := s.Current()
		types = append(types, ev.Type)
		if ev.ContentBlockDelta != nil && ev.ContentBlockDelta.Delta.Type == "text_delta" {
			text += ev.ContentBlockDelta.Delta.Text
		}
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"message_start", "content_block_start", "ping", "content_block_delta", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if !reflect.DeepEqual(types, want) {
		t.Errorf("events %v", types)
	}
	if text != "The sea is wide and grey." {
		t.Errorf("text %q", text)
	}
	final, err := s.FinalMessage()
	if err != nil {
		t.Fatal(err)
	}
	if final.Content[0].Text != "The sea is wide and grey." || *final.StopReason != "end_turn" || final.Usage.OutputTokens != 9 || final.Usage.InputTokens != 14 {
		t.Errorf("final %+v", final)
	}
}

func TestMessagesStreamRendered(t *testing.T) {
	c, tr := fixtureClient(t, 0, "messages-stream-rendered")
	s, err := c.Messages.CreateStreaming(ctx, MessageCreateParams{Model: "orbtr-model-1", MaxTokens: Int(256), Messages: sea})
	if err != nil {
		t.Fatal(err)
	}
	text, err := s.FinalText()
	if err != nil || text != "The sea is wide and grey." {
		t.Fatalf("%q %v", text, err)
	}
	assertSent(t, tr.sent[0], "messages-stream-rendered")
}

func TestCountTokens(t *testing.T) {
	c, tr := fixtureClient(t, 0, "messages-count-tokens")
	n, err := c.Messages.CountTokens(ctx, MessageCountTokensParams{Model: "orbtr-model-1", Messages: []MessageParam{UserMessage("How long is this request?")}})
	if err != nil {
		t.Fatal(err)
	}
	assertSent(t, tr.sent[0], "messages-count-tokens")
	if n.InputTokens != 14 {
		t.Errorf("input tokens %d", n.InputTokens)
	}
}

func TestFlowCalledAsAModelPinnedToAVersion(t *testing.T) {
	c, tr := fixtureClient(t, 0, "messages-create-flow", "messages-stream-flow")
	msg, err := c.Messages.Create(ctx, MessageCreateParams{
		Model: MustFlowModel("partition:p1:flow:council", 2), MaxTokens: Int(64), Messages: []MessageParam{UserMessage("review this")},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSent(t, tr.sent[0], "messages-create-flow")
	run := msg.Usage.Orbtr
	if msg.Content[0].Text != "the council agrees" || run == nil || run.Kind != "flow" || run.FlowID != "partition:p1:flow:council" || run.Version != 2 || run.TotalOT != 7 {
		t.Fatalf("flow reply %+v %+v", msg, run)
	}
	var sum int64
	for _, st := range run.Steps {
		sum += st.OT
	}
	if sum != run.TotalOT || run.Steps[0].ModelID != "orbtr-small" {
		t.Errorf("steps %+v", run.Steps)
	}
	s, err := c.Messages.CreateStreaming(ctx, MessageCreateParams{
		Model: MustFlowModel("partition:p1:flow:council", 0), MaxTokens: Int(64), Messages: []MessageParam{UserMessage("review this")},
	})
	if err != nil {
		t.Fatal(err)
	}
	final, err := s.FinalMessage()
	if err != nil {
		t.Fatal(err)
	}
	assertSent(t, tr.sent[1], "messages-stream-flow")
	if final.Content[0].Text != "the council agrees" || final.Usage.Orbtr == nil || final.Usage.Orbtr.RunID != "run1" {
		t.Errorf("streamed flow %+v", final)
	}
}

func TestChatCompletions(t *testing.T) {
	c, tr := fixtureClient(t, 0, "chat-completions-create", "chat-completions-stream", "chat-completions-create-flow")
	chat := []ChatMessageParam{ChatMessage("user", "Write one sentence about the sea.")}
	reply, err := c.Chat.Completions.Create(ctx, ChatCompletionCreateParams{Model: "orbtr-model-1", Messages: chat})
	if err != nil {
		t.Fatal(err)
	}
	assertSent(t, tr.sent[0], "chat-completions-create")
	if reply.Choices[0].Message.Content == nil || *reply.Choices[0].Message.Content != "The sea is wide and grey." || reply.Usage.TotalTokens != 23 {
		t.Errorf("reply %+v", reply)
	}
	s, err := c.Chat.Completions.CreateStreaming(ctx, ChatCompletionCreateParams{Model: "orbtr-model-1", Messages: chat})
	if err != nil {
		t.Fatal(err)
	}
	var text, finish string
	for s.Next() {
		ch := s.Current()
		text += ch.Choices[0].Delta.Content
		if ch.Choices[0].FinishReason != nil {
			finish = *ch.Choices[0].FinishReason
		}
	}
	if s.Err() != nil || text != "The sea is wide and grey." || finish != "stop" {
		t.Errorf("stream %q %q %v", text, finish, s.Err())
	}
	assertSent(t, tr.sent[1], "chat-completions-stream")
	flow, err := c.Chat.Completions.Create(ctx, ChatCompletionCreateParams{Model: "partition:p1:flow:council", Messages: []ChatMessageParam{ChatMessage("user", "review this")}})
	if err != nil {
		t.Fatal(err)
	}
	assertSent(t, tr.sent[2], "chat-completions-create-flow")
	if flow.Usage.Orbtr == nil || flow.Usage.Orbtr.Version != 2 {
		t.Errorf("flow usage %+v", flow.Usage)
	}
}

func TestCompletionsAndEmbeddings(t *testing.T) {
	c, tr := fixtureClient(t, 0, "completions-create", "embeddings-create")
	reply, err := c.Completions.Create(ctx, CompletionCreateParams{Model: "orbtr-model-1", Prompt: StringOf[string]("The sea is"), MaxTokens: Int(32)})
	if err != nil {
		t.Fatal(err)
	}
	assertSent(t, tr.sent[0], "completions-create")
	if reply.Choices[0].Text != "The sea is wide and grey." {
		t.Errorf("completion %+v", reply)
	}
	out, err := c.Embeddings.Create(ctx, EmbeddingCreateParams{Model: "orbtr-embed-1", Input: ArrayOf("the sea", "the sky")})
	if err != nil {
		t.Fatal(err)
	}
	assertSent(t, tr.sent[1], "embeddings-create")
	if len(out.Data) != 2 || !reflect.DeepEqual(out.Data[0].Embedding, []float64{0.25, -0.5, 0.125}) || out.Usage.PromptTokens != 6 {
		t.Errorf("embeddings %+v", out)
	}
}

func TestModelsList(t *testing.T) {
	c, tr := fixtureClient(t, 0, "models-list")
	models, err := c.Models.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertSent(t, tr.sent[0], "models-list")
	var ids []string
	for _, m := range models.Data {
		ids = append(ids, m.ID)
	}
	if !reflect.DeepEqual(ids, []string{"orbtr-embed-1", "orbtr-model-1", "orbtr-model-2"}) || models.Data[1].ContextLength != 131072 || models.Data[0].ContextLength != 0 {
		t.Errorf("models %+v", models.Data)
	}
}

func TestBatches(t *testing.T) {
	c, tr := fixtureClient(t, 0, "batches-create", "batches-create", "batches-list", "batches-retrieve", "batches-cancel", "batches-results")
	reqs := MessageBatchCreateParams{Requests: []MessageBatchRequest{
		{CustomID: "first", Params: MessageCreateParams{Model: "orbtr-model-1", MaxTokens: Int(64), Messages: []MessageParam{UserMessage("Say hello.")}}},
		{CustomID: "second", Params: MessageCreateParams{Model: "orbtr-model-2", MaxTokens: Int(64), Messages: []MessageParam{UserMessage("Say hello.")}}},
	}}
	batch, err := c.Messages.Batches.Create(ctx, reqs)
	if err != nil {
		t.Fatal(err)
	}
	assertSent(t, tr.sent[0], "batches-create")
	if !strings.HasPrefix(tr.sent[0].header.Get("Idempotency-Key"), "orbtr-sdk-") {
		t.Errorf("Idempotency-Key %q", tr.sent[0].header.Get("Idempotency-Key"))
	}
	if batch.ProcessingStatus != "in_progress" || batch.RequestCounts.Processing != 2 {
		t.Errorf("batch %+v", batch)
	}
	if _, err := c.Messages.Batches.Create(ctx, reqs, WithIdempotencyKey("nightly-2026-11-25")); err != nil {
		t.Fatal(err)
	}
	if tr.sent[1].header.Get("Idempotency-Key") != "nightly-2026-11-25" {
		t.Errorf("caller's key %q", tr.sent[1].header.Get("Idempotency-Key"))
	}
	page, err := c.Messages.Batches.List(ctx, MessagesBatchesListParams{Limit: Int(20)})
	if err != nil {
		t.Fatal(err)
	}
	assertSent(t, tr.sent[2], "batches-list")
	if page.HasMore || page.LastID != "msgbatch_sdk_01" {
		t.Errorf("page %+v", page)
	}
	one, err := c.Messages.Batches.Get(ctx, "msgbatch_sdk_01")
	if err != nil || one.ProcessingStatus != "ended" {
		t.Fatalf("get %+v %v", one, err)
	}
	assertSent(t, tr.sent[3], "batches-retrieve")
	canceled, err := c.Messages.Batches.Cancel(ctx, "msgbatch_sdk_01")
	if err != nil || canceled.ProcessingStatus != "canceling" {
		t.Fatalf("cancel %+v %v", canceled, err)
	}
	assertSent(t, tr.sent[4], "batches-cancel")
	results, err := c.Messages.Batches.Results(ctx, "msgbatch_sdk_01")
	if err != nil {
		t.Fatal(err)
	}
	assertSent(t, tr.sent[5], "batches-results")
	if tr.sent[5].header.Get("Accept") != "application/x-ndjson" {
		t.Errorf("Accept %q", tr.sent[5].header.Get("Accept"))
	}
	if len(results) != 2 || results[0].CustomID != "first" || results[0].Result.Type != "succeeded" || results[0].Result.Message.Content[0].Text != "Hello." ||
		results[1].Result.Type != "errored" || results[1].Result.Error.Type != "model_not_entitled" {
		t.Errorf("results %+v", results)
	}
}

func TestUnknownFlowIsNotFound(t *testing.T) {
	c, _ := fixtureClient(t, 0, "error-flow-not-found")
	_, err := c.Messages.Create(ctx, MessageCreateParams{Model: "partition:p1:flow:unknown", MaxTokens: Int(64), Messages: []MessageParam{UserMessage("review this")}})
	var apiErr *APIError
	if !errors.Is(err, ErrNotFound) || !errors.As(err, &apiErr) || apiErr.StatusCode != 404 || apiErr.Code != "not_found_error" {
		t.Fatalf("err %v", err)
	}
}

func TestRefusalShapesAreAuthenticationErrors(t *testing.T) {
	for name, code := range map[string]string{
		"error-messages-no-key":      "authentication_error",
		"error-chat-no-key":          "authentication_error",
		"error-missing-bearer-token": "missing_bearer_token",
	} {
		c, _ := fixtureClient(t, 0, name)
		_, err := c.Models.List(ctx)
		var apiErr *APIError
		if !errors.Is(err, ErrAuthentication) || !errors.As(err, &apiErr) || apiErr.Code != code {
			t.Errorf("%s: err %v", name, err)
		}
	}
}

func TestA429IsRetriedThenReturned(t *testing.T) {
	c, tr := fixtureClient(t, 1, "error-rate-limited", "error-rate-limited")
	var waits []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	_, err := c.Messages.Create(ctx, MessageCreateParams{Model: "orbtr-model-1", MaxTokens: Int(256), Messages: sea})
	var apiErr *APIError
	if !errors.Is(err, ErrRateLimit) || !errors.As(err, &apiErr) || apiErr.Code != "partition_inflight_exceeded" ||
		apiErr.Header.Get("X-Admission-Reject-Reason") != "partition_inflight_exceeded" {
		t.Fatalf("err %v", err)
	}
	if len(tr.sent) != 2 || len(waits) != 1 || waits[0] < 375*time.Millisecond || waits[0] > 500*time.Millisecond {
		t.Errorf("sent %d, waits %v", len(tr.sent), waits)
	}
}
