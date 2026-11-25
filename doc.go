// Package orbtr is the ORBTR SDK for Go: messages, streaming, models, flows as models, token counting,
// embeddings, OpenAI-compatible chat and completions, and message batches on api.orbtr.ai.
//
// The types and the service methods are generated from contract/orbtr-v1.json, the /v1 surface api.orbtr.ai
// serves; client.go, core.go, stream.go, errors.go and flows.go are the hand-written layer: the API key as a
// bearer token, the base URL, retries with backoff on 429 and 5xx honouring Retry-After, typed errors,
// server-sent event streams, and flows called as models by id and version.
//
//	client, err := orbtr.NewClient() // reads ORBTR_API_KEY
//	msg, err := client.Messages.Create(ctx, orbtr.MessageCreateParams{
//		Model:     "MODEL_ID",
//		MaxTokens: orbtr.Int(256),
//		Messages:  []orbtr.MessageParam{orbtr.UserMessage("Write one sentence about the sea.")},
//	})
package orbtr

//go:generate go run ./internal/cmd/generate
