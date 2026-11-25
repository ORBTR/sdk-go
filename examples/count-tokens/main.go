// Command count-tokens counts a request's input tokens before it is sent (docs.orbtr.ai, Recipes, "Count
// tokens before you send"). It needs ORBTR_API_KEY with the orbtr.inference.read scope, and ORBTR_MODEL.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	orbtr "github.com/ORBTR/sdk-go"
)

func main() {
	ctx := context.Background()
	client, err := orbtr.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	model := os.Getenv("ORBTR_MODEL")
	count, err := client.Messages.CountTokens(ctx, orbtr.MessageCountTokensParams{
		Model:    model,
		Messages: []orbtr.MessageParam{orbtr.UserMessage("How long is this request?")},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("input tokens: %d\n", count.InputTokens)

	// Compare it with the model's context_length from the model list. A request larger than the model's
	// context limit is refused with an error that names the limit; it is never cut short.
	models, err := client.Models.List(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, m := range models.Data {
		if m.ID == model && m.ContextLength > 0 {
			fmt.Printf("context length: %d\n", m.ContextLength)
		}
	}
}
