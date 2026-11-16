// Command voice shows the speech surfaces of the library: a speech-driven
// tool-call turn (CompleteAudio) and a plain transcription pass, both on
// the same shared engine and the same install as Needle.
//
//	go run ./examples/voice memo.wav
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/FlameInTheDark/cactus-go/needle"
	"github.com/FlameInTheDark/cactus-go/whistle"
)

type ReminderArgs struct {
	What string `cactus:"what" desc:"what to be reminded of" required:"true"`
	When string `cactus:"when" desc:"when to remind, ISO date" format:"date" required:"true"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Println("usage: voice <clip.wav>")
		fmt.Println("  any WAV works; try one with 'remind me to call mom on march 3rd'")
		os.Exit(2)
	}
	clip := os.Args[1]
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// 1. A speech-driven tool-call turn: the engine transcribes the clip
	//    and feeds the transcript through the tool grammar in one pass.
	agent, err := needle.New(needle.WithTools(needle.ToolFunc("set_reminder",
		"Schedule a reminder for the user.",
		func(ctx context.Context, a ReminderArgs) (any, error) {
			return map[string]any{"ok": true, "what": a.What, "when": a.When}, nil
		})))
	if err != nil {
		log.Fatal(err)
	}
	defer agent.Close()

	resp, err := agent.CompleteAudio(ctx, clip,
		needle.WithLanguage("en"),     // or empty to detect it
		needle.WithKeywords("remind"), // bias rare words
	)
	if err != nil {
		log.Fatal(err)
	}
	if resp.AudioText != nil {
		fmt.Printf("heard      %q\n", *resp.AudioText)
	}
	if call := resp.FirstCall(); call != nil {
		pretty, _ := json.MarshalIndent(call.Arguments, "", "  ")
		fmt.Printf("call       %s\narguments  %s\n", call.Name, string(pretty))
	} else {
		fmt.Println("call       none (off-topic, unsupported, or negated)")
	}
	if resp.Confidence != nil {
		fmt.Printf("confidence %.4f\n", *resp.Confidence)
	}

	// 2. A plain transcription pass with Whistle, on the same engine.
	result, err := whistle.Transcribe(ctx, clip, whistle.WithLanguage("en"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("transcript %q (language %s, ttft %.0f ms)\n",
		result.Text, result.Language, result.TTFTMS)
}
