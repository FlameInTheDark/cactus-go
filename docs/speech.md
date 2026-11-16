# Speech (Whistle)

Whistle is the speech-to-text model of the Cactus stack: transcription,
word timestamps, live streaming and speech embeddings, running on the same
shared engine and the same install as Needle — no extra download beyond
the `whistle.cact` weights, no separate process.

Like the other two models it exposes two shapes: **one-shot package
calls** (`whistle.Transcribe`, `whistle.Stream`, `whistle.Embed`) bound to
a shared default model, and a **constructed model** (`whistle.New`) for
repeated work or tuned weights.

- [Transcribing a clip](#transcribing-a-clip)
- [Languages](#languages)
- [Keywords](#keywords)
- [Word timestamps](#word-timestamps)
- [Live streaming](#live-streaming)
- [Speech embeddings](#speech-embeddings)
- [Audio input formats](#audio-input-formats)
- [Tuned weights](#tuned-weights)
- [The model type](#the-model-type)

## Transcribing a clip

One call, no construction:

```go
result, err := whistle.Transcribe(ctx, "memo.wav",
    whistle.WithLanguage("en"),       // or empty to detect it
)
fmt.Println(result.Text)
```

The result is one `Transcription`:

```go
type Transcription struct {
    Text      string   // the transcript; silence yields ""
    Language  string   // the detected (or forced) language code
    TTFTMS    float64  // time to first token, milliseconds
    DecodeTPS float64  // decode throughput, tokens per second
    Words     []Word   // per-word timings, only when WordTimestamps was set
}
```

One pass covers up to **30 seconds**; longer clips are windowed by the
engine. Silence and steady noise return empty text and an empty language
— treat that as "nothing was said", not as an error. Music can still come
out as text, so put a voice-activity detector or wake word in front of an
always-on microphone.

## Languages

The shipped model transcribes English, German, French, Spanish, Italian,
Dutch and Polish (`whistle.Languages`). Detection is automatic; force a
language with `WithLanguage` and an ISO code:

```go
result, _ := whistle.Transcribe(ctx, clip, whistle.WithLanguage("de"))
```

`whistle.LanguagesOf(weights)` reads the languages a specific archive
advertises from its tokenizer — useful before loading a tuned archive
whose language set differs from the shipped one.

## Keywords

`WithKeywords` biases transcription towards given words and phrases — the
fix for domain vocabulary and rare names the decoder undervalues:

```go
result, _ := whistle.Transcribe(ctx, "meeting.wav",
    whistle.WithKeywords("Siobhan", "Kubernetes", "Q3 roadmap"))
```

Phrases pass as arguments, or as one string with one phrase per line.

## Word timestamps

`WordTimestamps(true)` asks for per-word timings; `Words` then carries one
`Word` per token:

```go
result, _ := whistle.Transcribe(ctx, clip, whistle.WordTimestamps(true))
for _, w := range result.Words {
    fmt.Printf("%6.2f - %6.2f  %s (%.2f)\n", w.Start, w.End, w.Word, w.Probability)
}
```

`Start`/`End` are seconds from the beginning of the clip (or of the
stream, when live); `Probability` is the decode probability of that word.

## Live streaming

`Stream` transcribes audio of any length as it arrives. You supply an
`iter.Seq[[]float32]` of 16 kHz mono sample buffers — about a second of
audio per chunk is the sweet spot — and iterate the returned steps:

```go
chunks := micChunks(ctx) // your source: []float32 buffers, ~1s each

for step, err := range whistle.Stream(ctx, chunks, whistle.WithLanguage("en")) {
    if err != nil {
        break // engine or context error; the stream has stopped cleanly
    }
    fmt.Printf("\r%v … %s", step.Text, step.Pending)
}
```

Every chunk re-transcribes the last seconds of audio and commits what two
passes agree on. Each yielded `StreamStep` carries:

| Field | Meaning |
| --- | --- |
| `Text` | The words this chunk **committed** — join steps with a space. |
| `Words` | Per-word timings (with `WordTimestamps`), relative to the stream start. |
| `Pending` | The unconfirmed tail, likely to change on later chunks. |
| `Language` | The detected (or forced) language. |
| `Received` | Seconds of audio received so far. |
| `PassMS` | Milliseconds this pass took. |

Two operational notes:

- When the chunks sequence ends, one final step carries the tail; breaking
  out of the loop early stops the stream cleanly.
- The shared engine lock is held for the whole stream — no other engine
  call (Needle, Mirage, another Whistle) interleaves until it finishes.

## Speech embeddings

`Embed` returns the speech encoder's output for one clip: one row of
floats per 80 ms frame, flattened into a single slice. Clips longer than
30 seconds are rejected by the engine.

```go
vec, err := whistle.Embed(ctx, "sample.wav")
```

The package-level form reuses the shared default model; construct a
`Whistle` for repeated embedding work.

## Audio input formats

Every speech API accepts the same input shapes:

| Input | Meaning |
| --- | --- |
| `string` | A WAV file path. |
| `[]byte` | Raw little-endian float32 samples. |
| `[]float32` / `[]float64` | 16 kHz mono samples in `[-1, 1]`. |

WAV files may be PCM u8/s16/s24/s32 or IEEE float, mono or multi-channel,
at any sample rate — anything off 16 kHz is resampled in pure Go (a
Kaiser-windowed sinc filter, no cgo). Formats other than WAV must be
decoded to samples on your side first.

## Tuned weights

Run a tuned `whistle.cact` archive by constructing the model with
`WithWeights`:

```go
speech := whistle.New(whistle.WithWeights("my_whistle.cact"))
```

For one-off calls against a tuned archive without keeping a model around,
`TranscribeWith` and `StreamWith` take the archive path explicitly:

```go
result, err := whistle.TranscribeWith(ctx, "my_whistle.cact", clip)
```

See [Tuned weights](weights.md) for the worker-process machinery behind
tuned archives.

## The model type

`whistle.New` normalizes options and never fails — the engine is located
(and downloaded on first use) lazily by the first call. Construction
options:

| Option | Default | Effect |
| --- | --- | --- |
| `WithWeights(path)` | shipped `whistle.cact` | Run a tuned archive. |
| `WithEnginePath(path)` | resolution order | Pin the engine library (see [Engine management](engine.md)). |
| `WithBufferSize(n)` | 262144 | Initial JSON output buffer in bytes; grows with the audio automatically. |
| `WithLogger(logger)` | — | Log download and engine lifecycle events. |

A `Whistle` serializes its calls internally, and through the shared engine
with Needle and Mirage calls in the same process. `Weights()` reports the
resolved archive after the first call.

## Where to go next

- Speech driving tool calls: [Tool calling: speech-driven turns](tool-calling.md#speech-driven-turns)
- The CLI's transcription command: [CLI reference](cli.md#transcribe)
- Runnable code: [voice example](../examples/voice/main.go)
- Symbol-by-symbol: [API reference](api.md#package-cactuswhistle)
