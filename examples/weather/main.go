// Command weather is the smallest complete example: one zero-setup
// package-level Run with an inline toolset - no agent to construct,
// close or reset.
//
//	go run ./examples/weather
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/FlameInTheDark/cactus-go/needle"
)

type weatherArgs struct {
	City string `cactus:"city" desc:"city name" required:"true"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	getWeather := needle.ToolFunc("get_weather",
		"Get the current weather for a city.",
		func(ctx context.Context, args weatherArgs) (any, error) {
			return map[string]any{"city": args.City, "temp_c": 21, "sky": "clear"}, nil
		})

	// The one-shot Run: tools declared inline, handlers executed, results
	// collected - the zero-setup peer of whistle.Transcribe and
	// mirage.Describe.
	resp, err := needle.Run(ctx, "what's the weather in Paris?",
		needle.RunTools(getWeather))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("results:", resp.Results)

	// The same query without executing anything: package-level Complete
	// returns the call for the caller to run.
	resp, err = needle.Complete(ctx, "what's the weather in Rome?",
		needle.CompleteTools(getWeather))
	if err != nil {
		log.Fatal(err)
	}
	if call := resp.FirstCall(); call != nil {
		fmt.Println("call:", call.Name, call.Arguments)
	}
}
