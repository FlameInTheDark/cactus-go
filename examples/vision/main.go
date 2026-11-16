// Command vision shows the Mirage surfaces of the library: a structured
// description and a form-filling extraction, both on the same shared
// engine and the same install as Needle.
//
//	go run ./examples/vision photo.png
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/FlameInTheDark/cactus-go/mirage"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Println("usage: vision <image.(png|jpg|gif)>")
		os.Exit(2)
	}
	image := os.Args[1]
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	vision := mirage.New()

	// 1. A structured description: sections pick what comes back, fields
	//    pick the per-object details (boxes and confidence scores).
	result, err := vision.Describe(ctx, image,
		mirage.WithSections("objects", "text"),
		mirage.WithFields("bbox", "score"),
	)
	if err != nil {
		log.Fatal(err)
	}
	pretty, _ := json.MarshalIndent(result, "", "  ")
	fmt.Printf("description\n%s\n", pretty)

	// 2. Form filling: an ordered form constrains what the model answers,
	//    exactly like a JSON schema constrains a tool call.
	form, err := vision.Extract(ctx, image, map[string]any{
		"animal":  map[string]any{"enum": []any{"dog", "cat", "bird", "other"}},
		"count":   map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
		"setting": map[string]any{"type": "string"},
		"caption": map[string]any{"type": "string"},
	})
	if err != nil {
		log.Fatal(err)
	}
	pretty, _ = json.MarshalIndent(form, "", "  ")
	fmt.Printf("form\n%s\n", pretty)
}
