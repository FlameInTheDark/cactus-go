# Tuned weights

The native engine is weights-agnostic: **every model — base or tuned — is a
`.cact` archive loaded into the shared engine at runtime**, so a tuned
archive replaces the base model without recompiling anything. This page
covers running tuned archives and the process architecture that makes them
safe.

- [Running a tuned archive](#running-a-tuned-archive)
- [Format tags](#format-tags)
- [The worker process](#the-worker-process)
- [Confidence and tuned weights](#confidence-and-tuned-weights)
- [Lifecycle](#lifecycle)
- [Getting archives](#getting-archives)

## Running a tuned archive

One option per package:

```go
agent, err := needle.New(
    needle.WithWeights("my_needle.cact"),
    needle.WithTools(tools),
)
speech := whistle.New(whistle.WithWeights("my_whistle.cact"))
vision := mirage.New(mirage.WithWeights("my_mirage.cact"))
```

`needle.New` reports unreadable or mismatched archives immediately;
`whistle.New` and `mirage.New` only normalize options — a bad archive
surfaces as an error on the first call. Everything else works
identically — `Run`, `Complete`, `CompleteAudio`,
extraction, `Reset` — with the tuned weights driving
generation. The [weights example](../examples/weights/main.go) is a
complete program.

Multiple tuned agents with *different* archives coexist in one process
easily, because each one runs in its own worker process (below). A
base-model agent and a tuned agent also coexist.

## Format tags

Every `.cact` archive carries a format tag in its header; the library
reads it and refuses anything it cannot run on this engine:

| Tag | Meaning |
| --- | --- |
| `0x05E12A84` | Needle 3 generation — what this package runs |
| `0x05E12A83` | a Needle 2 archive — rejected with a clear error (use the older engine for those) |
| anything else | unknown format, rejected |

A mismatched or unreadable tag is reported by `New` (or
`ExtractWeights`) as an error naming the path.

`agent.Generation()` reports `3` for the base model and the generation of
its tuned archive, and `Tuned()` reports whether tuned weights are active.

## The worker process

The native engine keeps global state and **cannot unload weights** once
loaded. Loading a tuned archive in your own process would therefore pin
those weights forever. The library solves this with process isolation:

1. `WithWeights` starts a **worker process** — a re-exec of *your own binary*.
2. The child is started with a private `--cactus-worker <nonce>` argument
   plus a matching `CACTUS_GO_WORKER` environment value.
3. A package-level `init()` in cactus-go detects the handshake and takes
   the child over **before `main()` runs**, so applications never need to
   cooperate — importing the library is enough.
4. The child loads the engine, injects the archive with `cactus_load`,
   owns an independent engine and conversation, and serves requests over
   stdin/stdout with a length-prefixed JSON protocol. Speech turns load
   the whistle archive on demand inside the same child.

```
your process                         worker child (same binary)
    │ complete(query)                    │
    ├───────── JSON request ────────────▶│ engine + tuned weights
    │◀──────── JSON response ────────────┤ (own conversation)
```

Consequences worth knowing:

- **True isolation.** Each tuned agent has its own engine state; no
  turn-taking constraints between tuned agents (contrast
  [Conversations: concurrency](conversations.md#concurrency-and-multiple-agents)).
- **The nonce makes accidental activation impossible.** A stray
  `CACTUS_GO_WORKER` in the environment never hijacks a process: the argv
  value must match too.
- **Cancellation kills the child.** A cancelled `Run`/`Complete`
  propagates by terminating the worker — no zombie generation.
- **Re-exec means zero deployment friction.** No extra binary to ship;
  the worker is the application binary itself, launched with different
  arguments.
- `cactus.IsWorker()` reports whether the current process was started as
  a worker child — exported for applications that want to assert they
  never run in worker mode accidentally.

## Confidence and tuned weights

A response carries a calibrated `Confidence` score **exactly when the
archive ships a trained confidence head** — a small fp16 tensor in the
archive's directory whose code marks it as the head. The base archives
and platform fine-tunes keep the head (they are trained with it); a local
fine-tune built without a head reports `nil` instead of a meaningless
number, and the library logs a warning at startup when it detects that.

Gate on the score where present; gate tuned agents without a head on
domain validation instead (handler-side checks, schema bounds — both are
still fully enforced by the grammar).

## Lifecycle

- `Close()` is **required** for tuned agents — it shuts the worker
  process down. Base-model agents tolerate a missing `Close` (the engine
  unloads with the process); tuned agents leak a child process without
  it. `defer agent.Close()` covers every case and is safe to call twice.
- The worker inherits stdout/stderr hygiene: the library redirects the
  child's file descriptor 1 so stray native prints land on stderr instead
  of corrupting the protocol stream.

## Getting archives

Tuned archives come from training runs on your own data. The CLI can also
pull published archives from any Hugging Face repository:

```sh
cactus download <org>/<repo>/<file>.cact --out .
cactus download <org>/<repo>            # when the repo holds exactly one archive
```

The base model's own archive is a useful smoke test for the worker
machinery — it exercises the same load path with the same weights the
base model uses:

```sh
cactus download needle3 --out .
cactus run --weights needle3.cact --tools tools.json --prompt "..."
```

## Where to go next

- The tuning-free path: [Getting started](getting-started.md)
- Process and concurrency model: [Conversations](conversations.md#concurrency-and-multiple-agents)
- Download mechanics: [CLI reference](cli.md#download)
