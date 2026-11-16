# Large toolsets

Needle 3 has no tool retrieval head. Every declared tool schema is rendered
into the model's prompt in full, on every turn, and the decode grammar
covers the whole toolset — there is no top-five window and nothing is
selected per query. Needle 2's retrieval head lives on the legacy branch
(`cactus-compute<4`) and is not part of this engine generation.

- [What this means for a catalogue](#what-this-means-for-a-catalogue)
- [Designing a catalogue that stays accurate](#designing-a-catalogue-that-stays-accurate)
- [`WithToolIndexPath`](#withtoolindexpath)
- [A complete catalogue example](#a-complete-catalogue-example)

## What this means for a catalogue

- **The whole toolset is the prompt.** The engine embeds nothing, scores
  nothing and selects nothing: order and naming of the declared tools can
  change which one the model picks.
- **Cost grows with the catalogue.** Every turn prefills every schema, so
  a 40-tool catalogue pays 40 schemas of prefill on every query — watch
  `PrefillTPS` and `PrefixTokens` on the response.
- **The grammar still covers everything.** Constrained decoding compiles
  over the full toolset, so any declared tool is reachable on any turn —
  and a call is always schema-valid whichever tool it names.

## Designing a catalogue that stays accurate

The tuned model was trained on small, plainly named toolsets, and the
frozen acceptance suites run against five tools or fewer. Everything from
[Writing descriptions that work](tools.md) applies, plus catalogue-scale
rules:

1. **Keep the toolset small — five or fewer where the domain allows.**
   This is the shape every reference environment ships.
2. **Plain names, no shared prefixes.** `play_track`, `pause_playback`,
   `set_volume` — not `media_player__play_track`. A prefix shared by every
   tool hides the part that distinguishes them.
3. **Order is part of the prompt.** Declaration order is preserved in the
   schema bytes, and it can matter for borderline queries; declare the
   most central tools first and keep the order stable.
4. **Split overloaded tools.** One tool with a `mode` enum of twelve
   values picks worse than three tools with clear purposes.
5. **Descriptions should name the use cases.** The model reads them
   verbatim; the words in a description steer selection as much as the
   tool's name.

## `WithToolIndexPath`

```go
agent, err := needle.New(
    needle.WithTools(tools...),
    needle.WithToolIndexPath("tools.idx"),
)
```

`WithToolIndexPath` is **reserved for engine tool-index archives** — an
ABI slot the current engine generation accepts but does not act on. It is
passed straight to `cactus_init` and changes nothing about how the
toolset is rendered today; declaring it is harmless and forward
compatible.

## A complete catalogue example

The [catalog example](../examples/catalog/main.go) declares eight media
tools with the builder API — the whole catalogue rides in the prompt —
and runs three queries:

```go
agent, err := needle.New(needle.WithTools(tools)) // 8 tools, all in the prompt
fmt.Printf("declared %d tools (the whole catalogue rides in the prompt)\n", len(agent.Tools()))

for _, query := range []string{
    "play humble bundle by molly ten",
    "set the volume to 40",
    "play my late night playlist on shuffle",
} {
    resp, _ := agent.Complete(ctx, query)
    // resp.FirstCall().Name: play_track, set_volume, play_playlist
}
```

Run it with:

```sh
go run ./examples/catalog
```

## Where to go next

- Declaring the tools themselves: [Building tools](tools.md)
- Executing whatever got picked: [Tool calling](tool-calling.md)
- The metrics each turn reports: [Responses](responses.md#metrics)
