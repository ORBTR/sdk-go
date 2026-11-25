// Command chat-completions sends the same message through the OpenAI-compatible /v1/chat/completions route
// (docs.orbtr.ai, Recipes, "Send a message with curl" and "Use the OpenAI Python SDK"). It needs
// ORBTR_API_KEY and ORBTR_MODEL.
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
	reply, err := client.Chat.Completions.Create(ctx, orbtr.ChatCompletionCreateParams{
		Model:    os.Getenv("ORBTR_MODEL"),
		Messages: []orbtr.ChatMessageParam{orbtr.ChatMessage("user", "Write one sentence about the sea.")},
	})
	if err != nil {
		log.Fatal(err)
	}
	if c := reply.Choices[0].Message.Content; c != nil {
		fmt.Println(*c)
	}
}
