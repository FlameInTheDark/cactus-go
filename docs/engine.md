# Engine management

The inference engine is a native shared library (`libcactus.so` /
`libcactus.dll` / `libcactus.dylib`) that runs every Cactus model: Needle 3
(text, tool calling), Whistle (speech) and Mirage (vision). Weight archives
(`.cact` files) are loaded into it at runtime. This page documents how the
library finds the engine, downloads it, caches it, and what you can
configure at the process and machine level.

- [What the engine is](#what-the-engine-is)
- [Resolution order](#resolution-order)
- [The cache layout](#the-cache-layout)
- [Fetching explicitly](#fetching-explicitly)
- [Air-gapped / offline devices](#air-gapped--offline-devices)
- [Alpine (musl) and other libc flavours](#alpine-musl-and-other-libc-flavours)
- [Platform support](#platform-support)
- [Pinning the engine in code](#pinning-the-engine-in-code)
- [Environment variables](#environment-variables)
- [The standalone runner](#the-standalone-runner)

## What the engine is

One library per platform, published on the Hugging Face Hub as a wheel
whose payload is the shared library. The library never links it at build
time — it calls `dlopen`/`LoadLibrary` at runtime through
[purego](https://github.com/ebitengine/purego), which is why
`CGO_ENABLED=0` builds work and cross-compiling from any host is a plain
`GOOS=windows go build`.

The C surface is the published `cactus.h`: fourteen symbols
(`cactus_load`, `cactus_models`, `cactus_last_error`, `cactus_init`,
`cactus_complete`, `cactus_set_audio`, `cactus_set_threads`,
`cactus_reset`, `cactus_transcribe`,
`cactus_stream_transcribe_process`, `cactus_stream_transcribe_stop`,
`cactus_describe`, `cactus_embed`, and the error helper) — all of them
bound and called from Go, no cgo anywhere.

Because the engine keeps global state, every call is serialized through
one runtime lock, and a weight archive is loaded at most once per path
per process. A tuned archive runs in its own child process (a re-exec of
your binary), so several tuned agents with different weights coexist.

After the engine and weights are in place, **inference never touches the
network**.

## Resolution order

Every agent resolves its engine library on first use, in this order:

1. **`CACTUS_LIB_PATH`** — an explicit library file;
2. **a library placed next to your executable** — `libcactus.so` (or the
   `.dll` / `.dylib` equivalent) in the executable's directory;
3. **the cache** — `~/.cache/cactus/engine/<version>/<revision>/`
   on Linux and macOS, `%USERPROFILE%\.cache\cactus\engine\<version>\<revision>\`
   on Windows;
4. **a download** of the pinned engine release for the current platform
   from the Hugging Face Hub — a checksum-verified wheel from which only
   `cactus/libcactus.*` is extracted, atomically, into the cache.

`WithEnginePath(path)` in code pins step 1 outright (see
[below](#pinning-the-engine-in-code)).

Weights resolve the same way per model: `CACTUS_NEEDLE_WEIGHTS`,
`CACTUS_WHISTLE_WEIGHTS` or `CACTUS_MIRAGE_WEIGHTS`, then the shared cache
(checksum-verified), then a pinned-revision download. See
[Tuned weights](weights.md) for running custom archives.

## The cache layout

```
~/.cache/cactus/
├── engine/
│   └── 4.1.1/                       engine version
│       └── d2c5cd70…/                release revision
│           └── libcactus.so          the extracted shared library
└── models/
    ├── needle3/
    │   └── <revision>/needle3.cact   the base text model
    ├── whistle/
    │   └── <revision>/whistle.cact   the base speech model
    └── mirage/
        └── <revision>/mirage.cact    the base vision model
```

- One directory per version × revision — upgrading the engine never
  clobbers the previous build, and every download is verified against the
  SHA-256 pin before it is used.
- Downloads land as `.part` files and are renamed into place, so a killed
  download can never leave a half-written library or weights file that
  loads by accident.
- Deleting the cache is always safe: the next agent re-downloads.

## Fetching explicitly

The `fetch` package and the CLI both expose explicit fetching (no agent
required):

```sh
cactus fetch                          # engine for this machine, into the cache
cactus fetch --out /tmp/engine        # ... into a specific directory
cactus fetch --platform-tag manylinux2014_aarch64   # a build for another device
```

In code:

```go
path, err := fetch.FetchLibrary(ctx, "", destDir)   // "" = this machine
path, err := fetch.WeightsPath(ctx, fetch.Whistle)  // base speech weights
```

Progress reporting hooks into both: assign `fetch.ProgressHook` (a
`func(label string, total, done int64)`) before the first fetch, or watch
the CLI's live progress lines.

## Air-gapped / offline devices

For a device that must never attempt the network:

1. On a connected machine, fetch the engine for the *device's* platform:

   ```sh
   cactus fetch --platform-tag manylinux2014_aarch64
   cactus download needle3    # and whistle / mirage as needed
   ```

2. Copy the library and weights to the same cache paths on the device
   (`~/.cache/cactus/engine/…` and `~/.cache/cactus/models/…`), or drop
   them **next to the deployed executable**, or point `CACTUS_LIB_PATH`
   (and the `CACTUS_*_WEIGHTS` variables) at them.
3. On the device, export `HF_HUB_OFFLINE=1`. A missing engine or weights
   file then fails fast with a clear error instead of attempting a
   download.

The same flag works for applications that want fail-fast behavior even on
networked machines: with everything cached, `HF_HUB_OFFLINE=1` changes
nothing; without it, the first agent call errors immediately.

## Alpine (musl) and other libc flavours

On Linux the library detects musl userlands (via `/proc/self/maps` and the
`ld-musl` loader glob) and fetches the `musllinux_1_2_*` engine build
instead of `manylinux2014_*` — Alpine works out of the box. `cannot open
engine library` on Alpine almost always means a glibc build was placed
manually; delete the cached library and let the package re-fetch the
matching one.

Architectures without published wheels (linux/armv7, riscv64, …) fail
with a message pointing at the platform's `libcactus.a` in the Hub
repository for self-built engines.

## Platform support

| Platform | Engine wheel | Status |
| --- | --- | --- |
| linux/amd64 (glibc) | `manylinux2014_x86_64` | supported, auto-detected |
| linux/arm64 (glibc) | `manylinux2014_aarch64` | supported, auto-detected |
| linux/amd64, arm64 (musl) | `musllinux_1_2_*` | supported, auto-detected |
| windows/amd64 | `win_amd64` | supported, auto-detected |
| windows/arm64 | `win_arm64` | supported, auto-detected |
| macOS amd64 / arm64 | `macosx_11_0_*` | supported, auto-detected |
| other linux arches | — | manual: build from the platform's `libcactus.a` and set `CACTUS_LIB_PATH` |

The **library itself** (not the engine) cross-compiles to a wider matrix —
windows/amd64, windows/arm64, linux/amd64, linux/arm64, darwin/amd64,
darwin/arm64 and more — all with `CGO_ENABLED=0`; the narrower engine
table above is what auto-downloads.

## Pinning the engine in code

```go
agent, err := needle.New(
    needle.WithTools(tools),
    needle.WithEnginePath("/opt/vendor/libcactus.so"),
)
```

`WithEnginePath` overrides environment variables and the cache lookup —
useful for shipping an engine beside your binary under a known path, or
for test harnesses pinning a specific engine build. The whistle and
mirage packages take the same option, and `Extract` takes
`ExtractEnginePath`.

## Environment variables

| Variable | Effect |
| --- | --- |
| `CACTUS_LIB_PATH` | Pin the engine library for every model. |
| `CACTUS_NEEDLE_WEIGHTS` | Pin the Needle 3 weights archive. |
| `CACTUS_WHISTLE_WEIGHTS` | Pin the Whistle weights archive. |
| `CACTUS_MIRAGE_WEIGHTS` | Pin the Mirage weights archive. |
| `HF_HUB_OFFLINE=1` | Never download; fail fast when something is missing. |
| `HF_ENDPOINT` | Use a different Hub endpoint (e.g. `https://hf-mirror.com`). |
| `HTTP(S)_PROXY` | Honored by the Go HTTP stack for downloads. |

## The standalone runner

Besides the shared library, every platform folder in the pinned release
carries a **standalone engine runner** — a single `cactus` / `cactus.exe`
executable that serves HTTP by itself:

```sh
cactus download linux-x86_64 --out .
./linux-x86_64/cactus --tools tools.json --serve
```

- `POST /complete` with `{"input": "..."}` — one completion;
- `POST /reset` — rewind the conversation.

Useful for non-Go services that want the engine over a local socket, and
as a deployment smoke test. Downloads via the
[CLI](cli.md#download) mark the runner executable automatically.

## Where to go next

- The first-run flow: [Getting started](getting-started.md)
- The CLI that drives all of this: [CLI reference](cli.md)
- Download functions in code: [API reference](api.md)
