// Command flow-model calls a published flow as a model (docs.orbtr.ai, Concepts, "Flows as models"): the
// flow's id is the model, and @ and a version number pin a published version. It needs ORBTR_API_KEY,
// ORBTR_FLOW_ID and, to pin a version, ORBTR_FLOW_VERSION.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"

	orbtr "github.com/ORBTR/sdk-go"
)

func main() {
	ctx := context.Background()
	client, err := orbtr.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	version := 0 // the newest published version
	if v := os.Getenv("ORBTR_FLOW_VERSION"); v != "" {
		if version, err = strconv.Atoi(v); err != nil {
			log.Fatal(err)
		}
	}
	model, err := orbtr.FlowModel(os.Getenv("ORBTR_FLOW_ID"), version)
	if err != nil {
		log.Fatal(err)
	}
	msg, err := client.Messages.Create(ctx, orbtr.MessageCreateParams{
		Model:     model,
		MaxTokens: orbtr.Int(1024),
		Messages:  []orbtr.MessageParam{orbtr.UserMessage("Review this paragraph for tone.")},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(msg.Content[0].Text)

	// The usage carries the run: the version that answered, its total OT and one line per step.
	if run := msg.Usage.Orbtr; run != nil {
		fmt.Printf("flow %s version %d: %d OT over %d steps\n", run.FlowID, run.Version, run.TotalOT, len(run.Steps))
	}
}
