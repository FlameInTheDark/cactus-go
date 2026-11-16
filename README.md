# cactus-go

**Pure-Go clients for the Cactus on-device models: Needle 3 tool calling,
Whistle speech-to-text and Mirage vision — one shared native engine, one
install, no cgo.**

Text goes in, a grammar-constrained JSON tool call comes out. Speech goes
in, the same call comes out with the transcript. An image goes in, a
structured description comes back. Every model runs on one `libcactus`
shared library loaded with `dlopen`/`LoadLibrary` at runtime through
[purego](https://github.com/ebitengine/purego) — the only third-party
dependency of the library itself (the companion CLI adds
[urfave/cli](https://github.com/urfave/cli)) — so `CGO_ENABLED=0` builds
and cross-compilation work everywhere.

This library is a Go port of the [Cactus Compute Python
reference](https://github.com/Cactus-Compute/cactus) (cactus on PyPI).

```go
// zero setup: declare the tools inline, run the loop, read the results
resp, err := needle.Run(ctx, "what's it like in Lagos right now?",
    needle.RunTools(getWeather))
fmt.Println(resp.Results)
// [{city:Lagos temp_c:27 sky:clear}]
```

```go
// an agent holds a conversation across turns
agent, err := needle.New(needle.WithTools(getWeather))
resp, err := agent.Run(ctx, "what's it like in Lagos right now?")

// speech-driven tool calling, one pass
resp, err := agent.CompleteAudio(ctx, "memo.wav", needle.WithLanguage("en"))

// standalone speech-to-text and vision on the same engine, zero setup
text, err := whistle.Transcribe(ctx, "memo.wav", whistle.WithLanguage("en"))
form, err := mirage.Extract(ctx, "photo.png",
    map[string]any{"animal": map[string]any{"enum": []any{"dog", "cat"}}})
```

All three models expose the same two shapes: a type you construct for
multi-turn work (`needle.New`, `whistle.New`, `mirage.New`) and convenient
package-level calls for one-shot use (`needle.Run`, `needle.Complete`,
`needle.CompleteAudio`, `needle.Embed`, `needle.Extract`,
`whistle.Transcribe`, `whistle.Stream`, `mirage.Describe`,
`mirage.Extract`).

- **One engine, three models** — `libcactus.so` / `.dll` / `.dylib` runs
  Needle 3 (text), Whistle (speech) and Mirage (vision); weight archives
  (`.cact`) are loaded into it, at most once per archive per process.
- **Simple contract** — tool calls come back as structured data. A
  byte-level grammar compiled from your schemas constrains every token,
  so a call is always well-formed; an empty call list is the refusal
  contract for off-topic input — there is no free-text fallback.
- **Confidence-gated** — responses carry a calibrated confidence score
  whenever the archive ships a trained confidence head (the base models
  and platform fine-tunes do; set a threshold, act above it, escalate
  below it).
- **Grounded** — date arguments are licensed by years the conversation
  actually mentions; the engine flags values it does not consider
  evidenced, and strict mode refuses to execute them (numbers whose value
  literally occurs in the input are exempt).
- **Whole-toolset prompts** — every declared tool schema rides in the
  prompt; plain names and tight descriptions keep selection accurate.
- **Bounded memory** — a 35 MB base archive and a sub-1 MB engine, with
  the engine keeping a bounded conversation context and reporting its
  peak RAM on every turn.
- **Self-contained** — the engine and the base weights are downloaded
  once from the Hugging Face Hub into `~/.cache/cactus`
  (`$XDG_CACHE_HOME/cactus` when set) and verified
  against pinned checksums; inference never touches the network.

Runs natively on **Windows** (amd64, arm64), **Linux** (amd64, arm64 —
glibc or musl) and **macOS** (amd64, arm64). No interpreter, no scripting
runtime, no cgo.

## Installation

```sh
go get github.com/FlameInTheDark/cactus-go
```

```go
import (
    "github.com/FlameInTheDark/cactus-go/needle"
    "github.com/FlameInTheDark/cactus-go/mirage"
    "github.com/FlameInTheDark/cactus-go/whistle"
)
```

Cactus is the core — one shared engine, one install. The three models are
its subpackages, one per model: `needle` (tool calling and extraction),
`whistle` (speech) and `mirage` (vision). The root `cactus` package holds
what they share: engine-wide settings (`SetThreads`, `Models`), the
worker-process machinery and the version pins.

## Quickstart

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/FlameInTheDark/cactus-go/needle"
)

type GetWeatherArgs struct {
    City string `cactus:"city" desc:"city name" required:"true"`
}

func main() {
    getWeather := needle.ToolFunc("get_weather",
        "Get the current weather for a city.",
        func(ctx context.Context, args GetWeatherArgs) (any, error) {
            return map[string]any{"city": args.City, "temp_c": 27, "sky": "clear"}, nil
        })

    // One query, no agent to manage: the package-level Run declares its
    // tools inline and starts from a fresh conversation every call.
    resp, err := needle.Run(context.Background(),
        "what's it like in Lagos right now?", needle.RunTools(getWeather))
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(resp.Results)
}
```

For conversations that span turns, construct an agent instead and keep it
around:

```go
agent, err := needle.New(needle.WithTools(getWeather))
if err != nil {
    log.Fatal(err)
}
defer agent.Close()
resp, err := agent.Run(ctx, "what's it like in Lagos right now?")
```

The first call locates the engine and the base weights, downloading them
from the Hugging Face Hub if this is the machine's first use. Every later
call is fully local. See [Getting started](docs/getting-started.md) for
the line-by-line walkthrough and [Engine management](docs/engine.md) for
the cache layout and offline installs.

## Documentation

The full documentation lives in [`docs/`](docs/) and is organized by task
— open the page that matches what you are trying to do:

| I want to… | Read |
| --- | --- |
| Install and run my first agent, understand the first-run download | [Getting started](docs/getting-started.md) |
| Declare tools — struct tags, builders, raw JSON schemas, the full tag reference | [Building tools](docs/tools.md) |
| Execute tool calls — `Run` vs `Complete`, one-shot package calls, feeding results back | [Tool calling](docs/tool-calling.md) |
| Use a large catalogue — how the whole toolset rides in the prompt, naming rules, the reserved index slot | [Tool indexes](docs/tool-indexes.md) |
| Read everything a response carries — calls, confidence, validation, metrics | [Responses](docs/responses.md) |
| Hold multi-turn conversations, supply system facts, manage several agents | [Conversations](docs/conversations.md) |
| Turn unstructured text into typed structs | [Extraction](docs/extraction.md) |
| Transcribe clips, stream a microphone, embed speech | [Speech (Whistle)](docs/speech.md) |
| Describe images, read text, fill structured forms | [Vision (Mirage)](docs/vision.md) |
| Run tuned weights (`.cact` archives) | [Tuned weights](docs/weights.md) |
| Manage the engine — cache layout, offline devices, musl, environment variables | [Engine management](docs/engine.md) |
| Use the `cactus` command-line tool | [CLI reference](docs/cli.md) |
| Look up an exact signature | [API reference](docs/api.md) |
| Build, test and release the library itself — CI, conventional commits, release binaries | [Development & releases](docs/development.md) |

The speech and vision APIs are documented in their own guides —
[Speech](docs/speech.md) and [Vision](docs/vision.md) — with the full
symbol tables in the [API reference](docs/api.md) and on
[pkg.go.dev](https://pkg.go.dev/github.com/FlameInTheDark/cactus-go).

## Examples

Runnable programs for every feature live in [`examples/`](examples/):

| Example | Shows |
| --- | --- |
| `weather` | the smallest complete example: one zero-setup run with an inline toolset |
| `conversation` | manual loop with `Complete`, follow-ups, confidence gating, reset |
| `extraction` | typed, raw and strict extraction |
| `catalog` | a larger catalogue riding whole in the prompt |
| `smart_home` | a complete smart-home environment with a 32-case acceptance suite |
| `weights` | an agent running a tuned `.cact` archive |
| `voice` | speech-driven tool calls (`CompleteAudio`) plus a Whistle transcription |
| `vision` | Mirage description and form filling |

```sh
go run ./examples/weather
go run ./examples/voice memo.wav
go run ./examples/vision photo.png
```

## Project layout

```
cactus.go         the core: package doc, SetThreads, Models, model bits
version.go        Version, EngineGeneration, EngineVersion pins
worker.go         worker-mode process hook (init)
needle/           the text model: New, Complete, CompleteAudio, Run, Embed
needle/oneshot.go package-level Run / Complete / CompleteAudio / Embed,
                  the shared one-shot agent
needle/options.go With* / Run* / Complete* / Extract* option functions
needle/response.go Response, FunctionCall, Validation
needle/tool.go    Tool, ToolFunc, NewTool builder, Handler
needle/schema.go  struct-tag → JSON-Schema compiler
needle/property.go Property builders and constraint options
needle/rekey.go   engine-argument → Go-struct decoding
needle/grounding.go temporal grounding + strict validation
needle/extract.go Extract / ExtractAs / ExtractRaw
needle/cact.go    .cact archive inspection (confidence heads)
audio/            WAV parsing, resampling, option normalization
whistle/          speech-to-text, streaming, speech embeddings
mirage/           image description, OCR, forms, vision inputs
fetch/            engine + weights discovery, download, verification
internal/engine/  the cactus_* ABI, in-process backend, worker protocol
internal/testutil the C stub engine and fake .cact builders for tests
cmd/cactus/       the CLI (urfave/cli v3)
examples/         weather, extraction, conversation, catalog, smart_home,
                  weights, voice, vision
docs/             the documentation set
ANALYSIS.md       the Python-reference porting notes (format, ABI, grammar)
```

## Testing

```sh
go test ./...              # stub suite always; integration skips when offline
CACTUS_TEST_WEIGHTS=/path/to/needle3.cact go test -race ./...  # pin a tuned archive
go run ./examples/smart_home   # 32-case acceptance suite against the real engine
```

The suite has two layers: a deterministic offline layer that runs the
whole library against a C stub engine (the same approach as the Python
reference tests), and an integration layer that downloads the real engine
and weights on first use — `HF_HUB_OFFLINE=1` skips it cleanly on
air-gapped machines. CI runs this on every push to `main` on Linux and
Windows, and every release is cut from the commit history automatically —
see [Development & releases](docs/development.md).

## License

Apache License 2.0 — see [LICENSE](LICENSE).
