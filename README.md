# cactus-go

[![Go Reference](https://pkg.go.dev/badge/github.com/FlameInTheDark/cactus-go.svg)](https://pkg.go.dev/github.com/FlameInTheDark/cactus-go)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

Run small on-device models from Go: tool calling, speech-to-text and vision. No cgo, no Python, no server.

A Go port of [Cactus Compute](https://github.com/Cactus-Compute/cactus).

```go
resp, err := needle.Run(ctx, "what's it like in Lagos right now?",
    needle.RunTools(getWeather))

fmt.Println(resp.Results)
// [{city:Lagos temp_c:27 sky:clear}]
```

Text goes in, a well-formed tool call comes out, your Go function runs, and you get the result.

## Install

```sh
go get github.com/FlameInTheDark/cactus-go
```

Works on Windows, Linux (glibc and musl) and macOS, on both amd64 and arm64. Builds with `CGO_ENABLED=0`, so cross-compiling just works.

## Quick start

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/FlameInTheDark/cactus-go/needle"
)

// Tool arguments are a plain struct. Tags describe each field to the model.
type GetWeatherArgs struct {
    City string `cactus:"city" desc:"city name" required:"true"`
}

func main() {
    getWeather := needle.ToolFunc("get_weather",
        "Get the current weather for a city.",
        func(ctx context.Context, args GetWeatherArgs) (any, error) {
            return map[string]any{"city": args.City, "temp_c": 27, "sky": "clear"}, nil
        })

    resp, err := needle.Run(context.Background(),
        "what's it like in Lagos right now?", needle.RunTools(getWeather))
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(resp.Results)
}
```

The first run downloads the engine (under 1 MB) and the base model (35 MB) from the Hugging Face Hub into `~/.cache/cactus` and verifies their checksums. After that everything runs locally and offline.

## Conversations

`needle.Run` starts from a clean slate on every call. When the model needs to remember earlier turns, create an agent and keep it around:

```go
agent, err := needle.New(needle.WithTools(getWeather))
if err != nil {
    log.Fatal(err)
}
defer agent.Close()

resp, err := agent.Run(ctx, "what's it like in Lagos right now?")
resp, err = agent.Run(ctx, "and in Nairobi?")
```

## Speech and vision

The same engine also runs speech and image models.

```go
// Speech to text
text, err := whistle.Transcribe(ctx, "memo.wav", whistle.WithLanguage("en"))

// Speech straight to a tool call
resp, err := agent.CompleteAudio(ctx, "memo.wav", needle.WithLanguage("en"))

// Image to structured data
form, err := mirage.Extract(ctx, "photo.png", map[string]any{
    "animal": map[string]any{"enum": []any{"dog", "cat"}},
})
```

| Package   | Input          | Output                          | One-shot calls                                     |
| --------- | -------------- | ------------------------------- | -------------------------------------------------- |
| `needle`  | text or audio  | tool calls, extracted structs   | `Run`, `Complete`, `CompleteAudio`, `Extract`, `Embed` |
| `whistle` | audio          | transcript, speech embeddings   | `Transcribe`, `Stream`                             |
| `mirage`  | image          | description, OCR, filled forms  | `Describe`, `Extract`                              |

Each package also has a `New` constructor for when you want to hold the model open across many calls.

## Good to know

- Tool calls are always valid. Output is constrained by a grammar built from your tool schemas, so you never parse broken JSON.
- An empty call list means the model decided the request has nothing to do with your tools. There is no free-text fallback.
- Responses carry a confidence score when the weights include a confidence head, so you can act above a threshold and escalate below it.
- Date arguments the user never mentioned are flagged, and strict mode refuses to run them.

## Documentation

| I want to…                                      | Read                                         |
| ----------------------------------------------- | -------------------------------------------- |
| Get running and understand the first-run download | [Getting started](docs/getting-started.md) |
| Declare tools with struct tags, builders or raw JSON schemas | [Building tools](docs/tools.md)  |
| Execute calls and feed results back             | [Tool calling](docs/tool-calling.md)         |
| Work with a large set of tools                  | [Tool indexes](docs/tool-indexes.md)         |
| Read confidence, validation and metrics         | [Responses](docs/responses.md)               |
| Hold multi-turn conversations                   | [Conversations](docs/conversations.md)       |
| Turn text into typed structs                    | [Extraction](docs/extraction.md)             |
| Transcribe files or stream a microphone         | [Speech](docs/speech.md)                     |
| Describe images and fill forms                  | [Vision](docs/vision.md)                     |
| Use tuned `.cact` weights                       | [Tuned weights](docs/weights.md)             |
| Run offline, on musl, or manage the cache       | [Engine management](docs/engine.md)          |
| Use the `cactus` command-line tool              | [CLI](docs/cli.md)                           |
| Look up a signature                             | [API reference](docs/api.md) or [pkg.go.dev](https://pkg.go.dev/github.com/FlameInTheDark/cactus-go) |
| Build, test and release the library             | [Development](docs/development.md)           |

## Examples

Runnable programs live in [`examples/`](examples):

```sh
go run ./examples/weather            # smallest complete example
go run ./examples/conversation       # multi-turn loop with confidence gating
go run ./examples/extraction         # typed, raw and strict extraction
go run ./examples/voice memo.wav     # speech-driven tool calls
go run ./examples/vision photo.png   # image description and forms
go run ./examples/smart_home         # larger toolset with a 32-case acceptance suite
```

Also there: `catalog` (many tools at once) and `weights` (a tuned `.cact` archive).

## License

Apache 2.0, see [LICENSE](LICENSE).
