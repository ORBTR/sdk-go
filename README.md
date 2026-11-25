# ORBTR SDK for Go

`github.com/ORBTR/sdk-go` calls api.orbtr.ai: messages, streaming, models, flows as models, token counting,
embeddings, OpenAI-compatible chat and completions, and message batches.

Its types and service methods are generated from the API contract api.orbtr.ai serves
(`contract/orbtr-v1.json`); a small hand-written layer adds the API key, the base URL, streaming, retries and
typed errors. It uses only the standard library.

## Install

```bash
go get github.com/ORBTR/sdk-go
```

Go 1.22 or later. The package is `orbtr`.

## Make a client

```go
import orbtr "github.com/ORBTR/sdk-go"

client, err := orbtr.NewClient() // reads ORBTR_API_KEY
```

Make an API key at [manage.orbtr.ai/api_keys](https://manage.orbtr.ai/api_keys). The client sends it as
`Authorization: Bearer <key>`. Each call needs the key to carry the scope the call names
(`orbtr.inference.invoke` to send messages, `orbtr.inference.read` to list models and count tokens, and
`orbtr.messages.batch.*` for batches).

| Option | Default | What it does |
|---|---|---|
| `WithAPIKey` | `ORBTR_API_KEY` | The API key. |
| `WithBaseURL` | `ORBTR_BASE_URL`, then `https://api.orbtr.ai` | The API origin. |
| `WithMaxRetries` | `2` | Retries after a 429, a 5xx or a connection failure. |
| `WithTimeout` | 10 minutes | How long a call may take. |
| `WithHeader` | none | A header sent with every call. |
| `WithHTTPClient` | `http.DefaultClient` | The HTTP client to send through. |

## Send a message

```go
models, err := client.Models.List(ctx)
msg, err := client.Messages.Create(ctx, orbtr.MessageCreateParams{
	Model:     models.Data[0].ID,
	MaxTokens: orbtr.Int(256),
	Messages:  []orbtr.MessageParam{orbtr.UserMessage("Write one sentence about the sea.")},
})
fmt.Println(msg.Content[0].Text)
```

Optional fields are pointers (`orbtr.Int`, `orbtr.String`, `orbtr.Float`, `orbtr.Bool`, or `orbtr.Ptr`); a
field that is a string or an array, such as a message's content, is a `StringOr`, made with `StringOf` or
`ArrayOf`. Each reply keeps its JSON: `RawJSON()` returns it, with any field this version does not know.

## Stream a reply

```go
stream, err := client.Messages.CreateStreaming(ctx, orbtr.MessageCreateParams{
	Model:     "MODEL_ID",
	MaxTokens: orbtr.Int(512),
	Messages:  []orbtr.MessageParam{orbtr.UserMessage("Explain what a Space is in three sentences.")},
})
defer stream.Close()
for stream.Next() {
	if d := stream.Current().ContentBlockDelta; d != nil && d.Delta.Type == "text_delta" {
		fmt.Print(d.Delta.Text)
	}
}
if err := stream.Err(); err != nil { … }
```

The events follow the Anthropic Messages streaming format; each `MessageStreamEvent` has the field of its
`Type` set. `stream.FinalMessage()` gathers them into the whole reply. `client.Chat.Completions.CreateStreaming`
and `client.Completions.CreateStreaming` stream the OpenAI-compatible routes, ending at `[DONE]`.

## Call a flow as a model

A published flow answers as a model. Name its id as the `Model`; pin a published version with `FlowModel`:

```go
model, err := orbtr.FlowModel("partition:PARTITION:flow:NAME", 2) // "…@2"; 0 for the newest version
msg, err := client.Messages.Create(ctx, orbtr.MessageCreateParams{
	Model:     model,
	MaxTokens: orbtr.Int(1024),
	Messages:  []orbtr.MessageParam{orbtr.UserMessage("Review this paragraph for tone.")},
})
run := msg.Usage.Orbtr // the version that answered, the run's OT and one line per step
```

## Count tokens

```go
count, err := client.Messages.CountTokens(ctx, orbtr.MessageCountTokensParams{
	Model:    "MODEL_ID",
	Messages: []orbtr.MessageParam{orbtr.UserMessage("How long is this request?")},
})
fmt.Println(count.InputTokens)
```

## Embeddings, OpenAI-compatible calls and batches

```go
client.Embeddings.Create(ctx, orbtr.EmbeddingCreateParams{Model: "EMBEDDING_MODEL_ID", Input: orbtr.ArrayOf("the sea", "the sky")})
client.Chat.Completions.Create(ctx, orbtr.ChatCompletionCreateParams{
	Model: "MODEL_ID", Messages: []orbtr.ChatMessageParam{orbtr.ChatMessage("user", "Hello")},
})

batch, err := client.Messages.Batches.Create(ctx, orbtr.MessageBatchCreateParams{Requests: []orbtr.MessageBatchRequest{
	{CustomID: "first", Params: orbtr.MessageCreateParams{Model: "MODEL_ID", MaxTokens: orbtr.Int(64), Messages: []orbtr.MessageParam{orbtr.UserMessage("Hello")}}},
}})
results, err := client.Messages.Batches.Results(ctx, batch.ID) // one result per request
```

Creating a batch sends an `Idempotency-Key`, the same on every retry, so a retried create never makes a second
batch. Pass `orbtr.WithIdempotencyKey(key)` to choose it.

## Errors and retries

A refused call returns an `*orbtr.APIError` carrying `StatusCode`, `Code` (such as `invalid_api_key` or
`insufficient_ot`), `Message`, `Reason`, `Body` and `Header`. `errors.Is` matches it against its class:
`ErrBadRequest` (400), `ErrAuthentication` (401), `ErrPaymentRequired` (402), `ErrPermissionDenied` (403),
`ErrNotFound` (404), `ErrConflict` (409), `ErrUnprocessableEntity` (422), `ErrRateLimit` (429) and
`ErrInternalServer` (5xx).

```go
if errors.Is(err, orbtr.ErrRateLimit) { … }
var apiErr *orbtr.APIError
if errors.As(err, &apiErr) { log.Println(apiErr.Code) }
```

`*orbtr.ConnectionError` means no response arrived (`Timeout` says whether the call timed out);
`*orbtr.StreamError` means a stream failed after it began. A 429, a 5xx and a connection failure are retried up
to the client's retries, waiting as long as a `Retry-After` header asks (up to 60 seconds; a longer wait is
returned to you instead) or, without one, backing off from half a second to eight. Each call takes
`WithRequestMaxRetries`, `WithRequestTimeout` and `WithRequestHeader` to change this.

## Examples

`examples/` holds one program per recipe on [docs.orbtr.ai](https://docs.orbtr.ai/recipes/index):
`send-message`, `stream-reply`, `count-tokens`, `chat-completions` and `flow-model`.

## Developing

```bash
go test -race ./...                         # unit tests against recorded fixtures, then the integration test against a local fake
go generate ./...                           # regenerate types_gen.go and resources_gen.go from contract/orbtr-v1.json
ORBTR_CORTEX_DIR=<cortex checkout> go test -run TestContractMatchesCortex .
```

`fixtures/` holds HTTP exchanges recorded from api.orbtr.ai's handlers, each naming the handler it came from.
`TestContractMatchesCortex` compares each contract schema with the platform struct it names. Commits follow
Conventional Commits; release-please turns them into versions and `CHANGELOG.md`.

## Licence

Proprietary. See [LICENSE](LICENSE): you may use this module with an ORBTR account under ORBTR's Terms of
Service.
