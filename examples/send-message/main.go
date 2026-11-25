// Command send-message lists the model ids, chooses one, and sends a message (docs.orbtr.ai, Recipes, "Send
// a message with curl"). It needs ORBTR_API_KEY with the orbtr.inference.invoke and orbtr.inference.read
// scopes; ORBTR_MODEL chooses the model, otherwise the first listed model answers.
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

	// 1. List the model ids.
	models, err := client.Models.List(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, m := range models.Data {
		fmt.Println(m.ID)
	}

	// 2. Choose one.
	model := os.Getenv("ORBTR_MODEL")
	if model == "" && len(models.Data) > 0 {
		model = models.Data[0].ID
	}

	// 3. Send a message.
	msg, err := client.Messages.Create(ctx, orbtr.MessageCreateParams{
		Model:     model,
		MaxTokens: orbtr.Int(256),
		Messages:  []orbtr.MessageParam{orbtr.UserMessage("Write one sentence about the sea.")},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(msg.Content[0].Text)
}
