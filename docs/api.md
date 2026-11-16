# API reference

Complete reference for the `cactus` core and the `cactus/needle`,
`cactus/whistle`, `cactus/mirage`, `cactus/fetch` packages, and the CLI. The
[topic guides](index.md) cover concepts and examples; this page documents
every exported symbol.

Import paths:

```go
import (
    "github.com/FlameInTheDark/cactus-go"          // package cactus — the core
    "github.com/FlameInTheDark/cactus-go/needle"   // package needle — the text model
    "github.com/FlameInTheDark/cactus-go/whistle"  // package whistle — the speech model
    "github.com/FlameInTheDark/cactus-go/mirage"   // package mirage — the vision model
    "github.com/FlameInTheDark/cactus-go/fetch"    // package fetch — engine and weights
)
```

- [package cactus](#package-cactus) — the core
- [package cactus/needle](#package-cactusneedle) — the text model
  - [Needle](#needle)
  - [One-shot calls](#one-shot-calls)
  - [Construction options](#construction-options)
  - [Completion, run and extraction options](#completion-run-and-extraction-options)
  - [Response types](#response-types)
  - [Tool declaration](#tool-declaration)
  - [Property builders](#property-builders)
  - [Property constraint options](#property-constraint-options)
  - [Extraction](#extraction)
  - [Argument decoding](#argument-decoding)
  - [Schema compilation](#schema-compilation)
  - [Errors](#errors)
- [package cactus/whistle](#package-cactuswhistle)
- [package cactus/mirage](#package-cactusmirage)
- [package cactus/fetch](#package-cactusfetch)
- [cmd/cactus](#cmd-cactus)

## package cactus

The core of the stack: what the three model packages share. One native
engine runs Needle, Whistle and Mirage; this package configures it and
reports on it. Picking a model means picking its package, never this one —
but every program may import the core for the knobs below.

| Symbol | Description |
| --- | --- |
| `SetThreads(n int) error` | CPU thread count for every model loaded after the call, tuned worker children included (0 = automatic). |
| `Models(ctx) (int32, error)` | Bitmask of loaded model types: `ModelText` (1), `ModelSpeech` (2), `ModelVision` (4). Locates the engine the way the model packages do. |
| `IsWorker() bool` | Whether this process was started as a worker child of a tuned model session; exported so applications can assert they never run in worker mode accidentally. |
| `Version` | The library version (overridable at link time with `-ldflags`). |
| `EngineGeneration` | The engine generation this library targets (3). |
| `EngineVersion` | The pinned Cactus engine build the library downloads. |
| `ModelText`, `ModelSpeech`, `ModelVision` | The `cactus_models()` capability bits. |

## package cactus/needle

### Needle

```go
func New(opts ...Option) (*Needle, error)
```

Creates an agent bound to one toolset, one optional system turn and one
optional tuned weights archive — the text-model peer of `whistle.Whistle`
and `mirage.Mirage`. `New` only validates and normalizes its
inputs; the engine is located (or downloaded) lazily on first use. Errors from
`New`: malformed tool declarations, unserializable schemas, or an
unreadable/unknown `.cact` tag when `WithWeights` is used.

Needle methods:

| Method | Description |
| --- | --- |
| `Complete(ctx, text string, opts ...CompleteOption) (*Response, error)` | One conversation turn. Returns the engine's envelope; a pending call must be answered by feeding its result back with the next `Complete`. |
| `CompleteAudio(ctx, input any, opts ...CompleteOption) (*Response, error)` | One speech-driven turn: the engine transcribes the clip and feeds the transcript through the tool grammar in a single pass. `input` is a WAV path, raw float32 LE bytes or a sample slice; the envelope carries the transcript in `AudioText`. |
| `Run(ctx, query string, opts ...RunOption) (*Response, error)` | The full agentic loop: executes the declared handlers, feeds each round's results back, stops at `"respond"` or after max steps. The final response carries every executed result in `Results`. |
| `Embed(ctx, text string) ([]float32, error)` | The text embedding of the input from the model the agent runs on. |
| `Reset() error` | Rewinds the conversation, keeps the tools loaded. |
| `Close() error` | Releases the session; kills the worker process for tuned agents. Safe to call repeatedly. Required for tuned agents. |
| `ExtractRaw(ctx, text string, schema any, opts ...ExtractOption) (map[string]any, error)` | One-shot extraction reusing the agent's weights, engine path and system facts (fresh engine conversation). |
| `Tools() []string` | Declared tool names, in declaration order. |
| `Generation() int` | Engine generation in use (3 for the base model and Needle 3 archives). |
| `Tuned() bool` | Whether tuned weights are active. |
| `PrefixTokens() int` | Tool-prompt prefix token count of the last engine initialization. |

Behavioural notes:

- One agent per toolset; new tools means a new agent. An agent owns one
  conversation.
- The empty `FunctionCalls` list is the refusal contract for off-topic input —
  there is no free-text fallback.
- Calls are serialized; the engine's global state means at most one
  in-process session is bound per generation, and switching agents
  reconfigures the engine (rewinding the switched-from conversation).
- Once a tuned `.cact` is loaded into a process, the engine cannot unload it.
  The worker design isolates every tuned agent in its own child process, so
  this only surfaces if you load weights in-process yourself.

### One-shot calls

The zero-setup counterparts of the agent methods — the peers of
`whistle.Transcribe` and `mirage.Describe`. Each call runs on a shared
stateless agent, so every call starts from a fresh conversation and no
agent needs constructing, closing or resetting:

```go
func Run(ctx context.Context, query string, opts ...RunOption) (*Response, error)
func Complete(ctx context.Context, text string, opts ...CompleteOption) (*Response, error)
func CompleteAudio(ctx context.Context, input any, opts ...CompleteOption) (*Response, error)
func Embed(ctx context.Context, text string, opts ...Option) ([]float32, error)
```

| Function | Description |
| --- | --- |
| `Run` | The full agentic loop: declares its toolset inline with `RunTools`, executes the handlers, returns the final response with every result in `Results`. |
| `Complete` | One turn without executing: the envelope's calls are the caller's to run (or not). |
| `CompleteAudio` | One speech-driven turn; the envelope carries the transcript in `AudioText`. |
| `Embed` | The text embedding of the input; accepts the same options as `New` (`WithWeights`, `WithEnginePath`, `WithLogger`), none required. |

`Run` and `Complete`/`CompleteAudio` carry their agent identity in their own
option types; the loop- and turn-tuning options are shared with the agent
methods:

| Option | Applies to | Effect |
| --- | --- | --- |
| `RunTools(tools ...any)` | `Run` | Declares the toolset (same accepted forms as `WithTools`; `ToolFunc` tools carry their handlers). |
| `RunToolsJSON(s)` | `Run` | Declares the toolset from a JSON schema array. |
| `RunSystem(s)` | `Run` | System facts for the run. |
| `RunWeights(path)` | `Run` | Run against a tuned `.cact`. |
| `RunEnginePath(path)` | `Run` | Pin the engine library. |
| `RunLogger(logger)` | `Run` | Debug logging for the run. |
| `CompleteTools(tools ...any)` | `Complete`, `CompleteAudio` | Declares the toolset. |
| `CompleteToolsJSON(s)` | `Complete`, `CompleteAudio` | Declares the toolset from a JSON schema array. |
| `CompleteSystem(s)` | `Complete`, `CompleteAudio` | System facts for the turn. |
| `CompleteWeights(path)` | `Complete`, `CompleteAudio` | Turn against a tuned `.cact`. |
| `CompleteEnginePath(path)` | `Complete`, `CompleteAudio` | Pin the engine library. |
| `CompleteLogger(logger)` | `Complete`, `CompleteAudio` | Debug logging for the turn. |

```go
resp, err := needle.Run(ctx, "email finance@example.com about expenses",
    needle.RunTools(sendEmail), needle.RunSystem("date: 2026-11-16"))
vec, err := needle.Embed(ctx, "a red umbrella")
```

Sharing: one shared agent serves all one-shot calls (the `Extract` family
included), keyed by everything that configures the engine session —
toolset, system facts, weights, engine path, tool-index path, buffer,
stateless flag, logger. A call with
the same identity reuses it; a call with a new identity closes the previous
agent and initializes a fresh one, exactly as the Python reference's
`extract()` shares its agent. Alternating identities therefore
re-initializes on each switch — for repeated multi-turn work on one
toolset, construct a `Needle` instead.

### Construction options

| Option | Effect |
| --- | --- |
| `WithTools(tools ...any)` | Declares the toolset: `*Tool` values, raw schema maps, a JSON string, or a slice of any of these, mixed. |
| `WithToolsJSON(toolsJSON string)` | Declares the toolset from the engine's native JSON array of schemas. |
| `WithSystem(system string)` | Attaches environment facts (`date:`, `locale:`, `device:`, ...). |
| `WithAutoDate(false)` | Opt out of the automatic `date:` system fact. |
| `WithWeights(path string)` | Runs a tuned `.cact` in its own worker process. |
| `WithToolIndexPath(path string)` | Reserved passthrough for engine tool-index archives. |
| `WithBufferSize(size int)` | Response buffer size in bytes (default 65536). |
| `WithEnginePath(path string)` | Pins the engine library, overriding env vars and the cache. |
| `WithStateless(true)` | Reset before every turn, so each query is answered independently. |
| `WithLogger(logger *slog.Logger)` | Debug logging for engine lifecycle and downloads. |

### Completion, run and extraction options

| Option | Applies to | Effect |
| --- | --- | --- |
| `MaxTokens(n)` | `Complete`, `CompleteAudio` | Response token cap (default 512). |
| `WithLanguage(code)` | `CompleteAudio` | Transcription language, e.g. `"en"` (default: detect). |
| `WithKeywords(phrases...)` | `CompleteAudio` | Bias transcription towards the given words and phrases. |
| `WordTimestamps(bool)` | `CompleteAudio` | Ask for per-word timings. |
| `MaxSteps(n)` | `Run` | Tool-call round cap (default 8). |
| `RunMaxTokens(n)` | `Run` | Per-round token cap (default 512). |
| `RunStrict(false)` | `Run` | Execute calls even when arguments are flagged ungrounded (default strict). |
| `ExtractSystem(s)` | `Extract*` | System facts for the one-shot agent. |
| `ExtractMaxTokens(n)` | `Extract*` | Token cap (default 512). |
| `ExtractStrict(false)` | `Extract*` | Return ungrounded values instead of raising (default strict). |
| `ExtractWeights(path)` | `Extract*` | Extract against a tuned `.cact`. |
| `ExtractEnginePath(path)` | `Extract*` | Pin the engine library. |
| `ExtractLogger(logger)` | `Extract*` | Debug logging for the one-shot extraction agent. |

### Response types

```go
type Response struct {
    Type            string         // "call" | "respond" | engine error kinds
    Success         bool
    Error           *string
    ErrorCode       *string
    Reason          *string
    FunctionCalls   []FunctionCall
    SuppressedCalls []FunctionCall  // withheld: confidence below 0.1 or a grounding gate fired
    AudioText       *string         // transcript of an audio-driven turn
    Reasoning       *string
    Confidence      *float64        // nil = uncalibrated
    PrefillTPS      *float64
    DecodeTPS       *float64
    PeakRAMMB      *float64
    Validation      *Validation
    Results         []any           // attached by Run
}

type FunctionCall struct {
    Name      string
    Arguments map[string]any
}

type Validation struct {
    Ungrounded []string // "tool.field" paths
    Negation   bool
}
```

Response helpers:

| Method | Description |
| --- | --- |
| `HasCalls() bool` | At least one requested call. |
| `FirstCall() *FunctionCall` | First call, or nil (the refusal case). |
| `FirstCallOrSuppressed() *FunctionCall` | First call, falling back to the first suppressed one. |
| `Refused() bool` | No calls — off-topic, unsupported, ambiguous or negated input. |
| `UngroundedOf(tool string) []string` | Ungrounded argument paths reported for that tool's calls. |
| `String() string` | Compact JSON of the whole envelope. |

### Tool declaration

```go
type Handler func(ctx context.Context, arguments map[string]any) (any, error)
```

Handlers return the value fed back to the model and attached to `Results`. An
error maps to a `{"error": "<message>"}` result the model sees and can recover
from; panics are recovered and reported the same way. Values that fail to
marshal fall back to their `fmt` representation.

```go
func ToolFunc[A any](name, description string, fn func(ctx context.Context, args A) (any, error)) *Tool
```

Typed tool: `A`'s struct tags define the schema; arguments arrive decoded into
`A`. Nested structs, slices, embedded structs, pointers (optional fields) and
`time.Time` are all supported. If `A` is not a struct the handler receives an
error at invoke time.

```go
func NewTool(name, description string) *ToolBuilder
```

Dynamic builder:

| Method | Description |
| --- | --- |
| `Params(props map[string]Property)` | Parameters from named properties (required collected from `IsRequired`). |
| `RawParams(schema map[string]any)` | Parameters from a hand-written JSON-Schema object. |
| `Handler(fn Handler)` | Attaches a dynamic handler over the raw argument map. |
| `Triggers(patterns ...string)` | Case-insensitive regexes that route a matching request to this tool and require a call; a triggered call ships even below the confidence floor (shipped as a `"triggers"` schema key). |
| `Build() *Tool` | Freezes the tool. |

`*Tool` methods:

| Method | Description |
| --- | --- |
| `Name() string` | Call name. |
| `Description() string` | Description. |
| `HasHandler() bool` | Whether `Run` can execute this tool. |
| `Schema() map[string]any` | The engine-facing `{"name","description","parameters"}` object. |
| `Invoke(ctx, arguments) (any, error)` | Executes the handler with panic recovery; handlerless tools return an "unknown tool" error. |

### Property builders

```go
type Property map[string]any
type PropOption func(Property)
```

| Constructor | JSON type |
| --- | --- |
| `Str(opts...)` | string |
| `Int(opts...)` | integer |
| `Num(opts...)` | number |
| `Bool(opts...)` | boolean |
| `Any(opts...)` | unconstrained |
| `Enum(values ...any)` | type inferred from the first value |
| `ArrayOf(items Property, opts...)` | array |
| `ObjectOf(props map[string]Property, opts...)` | nested object |

```go
func Params(props map[string]Property) map[string]any
```

Assembles the parameters object; properties marked `IsRequired` are collected
into `required` (deterministically sorted for builders — struct-tag schemas
keep declaration order).

### Property constraint options

`Desc`/`Description`, `IsRequired`, `Min`, `Max`, `GT`, `LT`, `Range`,
`MultipleOf`, `MinLen`, `MaxLen`, `Pattern`, `Format`, `MinItems`,
`MaxItems`, `Unique`, `Const`, `Default`, `EnumValues` — each maps onto the
JSON-Schema key of the same meaning; see the
[tag reference](tools.md#the-tag-reference) for the struct-tag spellings.

### Extraction

```go
func Extract[A any](ctx context.Context, text string, opts ...ExtractOption) (*A, error)
func ExtractAs[A any](ctx context.Context, text string, schema any, opts ...ExtractOption) (*A, error)
func ExtractRaw(ctx context.Context, text string, schema any, opts ...ExtractOption) (map[string]any, error)
```

`Extract` derives the schema from `A`'s tags. `ExtractAs`/`ExtractRaw` accept
a `*Tool`, a schema map (full tool schema or bare parameters object), or a
JSON string. All return `(nil, nil)` when nothing matched. Strict mode raises
`ExtractionValidationError` for temporal contradictions, engine-reported
ungrounded paths, and negated requests.

### Argument decoding

```go
func DecodeArguments[A any](arguments map[string]any) (A, error)
```

Decodes engine arguments into a typed struct using the same cactus-tag →
field-name mapping as `ToolFunc`, recursing through nested structs, pointers
and slices. Use it when driving the loop by hand over `Complete`.

### Schema compilation

```go
func SchemaFromStruct[A any]() (map[string]any, error)
```

Compiles a struct type into a JSON-Schema parameters object without creating
a tool. Handy for inspecting what the engine will see, or for feeding schemas
into other systems.

### Errors

| Symbol | Meaning |
| --- | --- |
| `ExtractionValidationError` | Sentinel wrapped by strict-mode extraction failures. |
| `ErrClosed` | Operation on a closed Needle. |

`Version`, `EngineGeneration` and `EngineVersion` live in the core
[package cactus](#package-cactus).

## package cactus/whistle

Speech-to-text on the shared engine, in English, German, French, Spanish,
Italian, Dutch and Polish.

| Symbol | Description |
| --- | --- |
| `New(opts ...Option) *Whistle` | Creates a model; `WithWeights`, `WithEnginePath`, `WithBufferSize`, `WithLogger` accepted. |
| `(*Whistle).Transcribe(ctx, input, opts ...) (*Transcription, error)` | One clip to text; `input` is a WAV path or 16 kHz mono samples. One pass covers up to 30 s. |
| `(*Whistle).Stream(ctx, chunks, opts ...) iter.Seq2[*StreamStep, error]` | Live transcription of any length; the sequence yields one step per chunk plus a tail step; breaking out stops the stream cleanly. |
| `(*Whistle).Embed(ctx, input) ([]float32, error)` | Speech encoder output: one row per 80 ms frame, flattened. |
| `(*Whistle).Weights() string` | The weights archive in use (resolved after the first call). |
| `Languages` | The languages the shipped model transcribes. |
| `LanguagesOf(weights string) []string` | The languages an archive advertises, read from its tokenizer. |
| `Transcribe`, `Stream`, `Embed` | Package-level forms bound to the shared default model. |
| `TranscribeWith` / `StreamWith` | The same against an explicit tuned archive. |

Transcription options: `WithLanguage`, `WithKeywords`, `WordTimestamps`.

Result types:

```go
type Word struct {          // one transcribed word
    Word        string      // the word
    Start, End  float64     // seconds from the start of the clip/stream
    Probability float64
}

type Transcription struct { // one transcription pass
    Text      string        // silence yields ""
    Language  string        // detected, or the forced code
    TTFTMS    float64       // time to first token
    DecodeTPS float64       // decode throughput
    Words     []Word        // only when WordTimestamps was set
}

type StreamStep struct {    // one live-transcription update
    Text     string         // words this chunk committed; join steps with a space
    Words    []Word         // timings relative to the stream start
    Pending  string         // unconfirmed tail
    Language string
    Received float64        // seconds of audio received so far
    PassMS   float64        // milliseconds this pass took
}
```

The [Speech](speech.md) guide covers transcription, streaming and embeddings
end to end.

## package cactus/mirage

Vision on the shared engine: description, OCR, object detection with masks,
and structured form filling.

| Symbol | Description |
| --- | --- |
| `New(opts ...Option) *Mirage` | Creates a model; `WithWeights`, `WithEnginePath`, `WithBufferSize`, `WithLogger` accepted. |
| `(*Mirage).Describe(ctx, input, opts ...) (map[string]any, error)` | One image to a structured JSON result. |
| `(*Mirage).Extract(ctx, input, form, opts ...) (map[string]any, error)` | Fills an ordered form: `enum` choices, bounded integers, free strings, `x-source` copies. |
| `(*Mirage).Weights() string` | The weights archive in use (resolved after the first call). |
| `ToRGB(input) (*RGB, error)` | Converts a path, PNG/JPEG/GIF bytes, `image.Image` or `mirage.RGB` into packed RGB. |
| `Describe`, `Extract` | Package-level forms bound to the shared default model. |

Image inputs and the `RGB` type:

```go
type RGB struct {           // packed 8-bit image data
    Width, Height int
    Data          []byte    // Width*Height*3 bytes, row-major
}
```

Every vision input is normalized through `ToRGB`: a file path (PNG, JPEG,
GIF — JPEGs auto-rotate by their EXIF orientation), encoded bytes of the
same formats, a Go `image.Image`, or a ready `*RGB`. Pass `*RGB` when you
already hold decoded pixels to skip a re-decode. The [Vision](vision.md)
guide covers description, OCR and forms end to end.

Describe options: `WithSections`, `WithFields`, `WithConf`, `WithTextConf`,
`WithMaxObjects`, `WithMaxTextLines`, `WithMaxTokens`, `WithMaskFormat`,
`WithNMS`, `WithForm`, `WithOptions`, `WithLanguage`, `WithKeywords`. A key
supplied both in `WithOptions` and as an explicit setting is an error.
Sections are `scene`, `objects`, `regions`, `text`, `relations`, `events`
and `reasoning`; per-object fields are `bbox`, `score`, `point`, `position`,
`description`, `count` and `mask`; mask formats are `largest`, `merged`,
`polygons` and `bitmap`. The engine rejects unknown names - `conf` and
`text_conf` are threshold settings, not per-object fields.

## package cactus/fetch

Engine and weights discovery and downloads. All functions take a
`context.Context` and honor `HF_ENDPOINT`, `HF_HUB_OFFLINE` and the standard
proxy variables.

| Symbol | Description |
| --- | --- |
| `LibraryPath(ctx) (string, error)` | Full resolution order for the engine: `CACTUS_LIB_PATH` → executable directory → cache → download. The entry point the agents use. |
| `WeightsPath(ctx, model) (string, error)` | Resolves a model's base weights (`Needle3`, `Whistle`, `Mirage`): env override → checksum-verified cache → pinned download. |
| `FetchLibrary(ctx, tag, destDir) (string, error)` | Downloads the engine wheel for a platform tag (current machine when empty), verifies it against the pinned release manifest, extracts the shared library into destDir, returns its path. |
| `DownloadPlatform(ctx, platform, outDir) ([]string, error)` | Copies a platform folder's standalone runner files (`linux-x86_64`, ..., `wasm`, `wasm-component`) into `outDir/<platform>/`, marking native runners executable. |
| `DownloadWeights(ctx, spec, outDir) (string, error)` | Pulls `<org>/<repo>/<file>.cact` (or the single archive of `<org>/<repo>`) into outDir. |
| `EngineCacheDir() (string, error)` | The engine cache directory. |
| `ModelCacheDir(model) (string, error)` | The base-weights cache directory of a model. |
| `PlatformTag() (string, error)` | Platform tag for the current machine. |
| `PlatformTagFor(goos, goarch) (string, error)` | Platform tag for an explicit pair. |
| `LibName() string` | `libcactus.so` / `.dll` / `.dylib` for the current GOOS. |
| `LibNameForTag(tag string) string` | Library file name inside a wheel built for the given platform tag. |
| `EngineRepo` / `EngineVersion` | The pinned Hub repository (`Cactus-Compute/cactus`) and build version. |
| `EngineRevision`, `ManifestSHA256` | The pinned release commit and manifest checksum. |
| `EngineReleaseDir` | The release directory inside the Hub repository (`releases/<version>`). |
| `ModelRepos`, `ModelRevisions`, `ModelSHA256` | The per-model pins behind `WeightsPath`. |
| `BaseWeights` | Archive file name of each model inside its repository (`needle3.cact`, ...). |
| `WeightsEnv` | Model key → weights-path environment variable (`CACTUS_NEEDLE_WEIGHTS`, ...). |
| `WheelTags` | The wheel platform tags a library download can pick from. |
| `Platforms` | The list of standalone-runner platform folders. |
| `ValidModel(key)` / `CheckModel(key)` | Whether a model key is fetchable, with a friendly error otherwise. |
| `Endpoint() string` | Active Hub endpoint (honors `HF_ENDPOINT`). |
| `DefaultEndpoint` | The default Hub endpoint constant (`https://huggingface.co`). |
| `Offline() bool` | Whether `HF_HUB_OFFLINE` is set. |
| `ProgressHook ProgressFunc` | Optional progress callback, invoked during downloads. |
| `ProgressFunc` | `func(label string, total, done int64)`; `total` is -1 without a content length. |
| `UserAgent` | The HTTP user agent (override before first use if you like). |

## cmd/cactus

```
cactus fetch    [--out DIR] [--platform-tag TAG]
cactus download <needle3 | whistle | mirage | platform | org/repo[/file].cact> [--out DIR]
cactus run      [--prompt TEXT] [--audio FILE] [--language CODE] [--keywords LIST] [--tools FILE] [--system TEXT] [--weights FILE] [--max N] [--json] [--timeout D]
cactus transcribe [--language CODE] [--keywords LIST] [--word-timestamps] [--weights FILE] [--json] [--timeout D] <clip.wav>
cactus describe [--sections LIST] [--fields LIST] [--conf N] [--form FILE] [--language CODE] [--keywords LIST] [--weights FILE] [--json] [--timeout D] <image.png>
cactus version
```

Positional arguments may appear before or after flags. `run` answers one
prompt through the library itself, so it exercises the same cache and download
path your application will. See the [CLI reference](cli.md) for the full flag
documentation.
