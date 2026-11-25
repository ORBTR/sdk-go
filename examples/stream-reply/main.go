// Command stream-reply receives a reply as server-sent events while it is written (docs.orbtr.ai, Recipes,
// "Stream a reply"). It needs ORBTR_API_KEY and ORBTR_MODEL.
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
	stream, err := client.Messages.CreateStreaming(ctx, orbtr.MessageCreateParams{
		Model:     os.Getenv("ORBTR_MODEL"),
		MaxTokens: orbtr.Int(512),
		Messages:  []orbtr.MessageParam{orbtr.UserMessage("Explain what a Space is in three sentences.")},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer stream.Close()

	// The text arrives in content_block_delta events, and message_stop ends the stream.
	for stream.Next() {
		if d := stream.Current().ContentBlockDelta; d != nil && d.Delta.Type == "text_delta" {
			fmt.Print(d.Delta.Text)
		}
	}
	if err := stream.Err(); err != nil {
		log.Fatal(err)
	}
	fmt.Println()
}
