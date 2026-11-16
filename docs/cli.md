# CLI reference

The repository ships a companion CLI, built with
[urfave/cli v3](https://github.com/urfave/cli).

## Installing

**Prebuilt binary.** Every [GitHub
release](https://github.com/FlameInTheDark/cactus-go/releases) publishes
ready-to-run archives for all supported platforms, plus a `checksums.txt`.
Download the one matching your machine, verify it, extract it and put the
binary on your `PATH`:

```sh
curl -fL -o cactus.tar.gz \
  https://github.com/FlameInTheDark/cactus-go/releases/latest/download/cactus_<version>_linux_amd64.tar.gz
tar xzf cactus.tar.gz cactus_<version>_linux_amd64/cactus
./cactus_<version>_linux_amd64/cactus version
```

On Windows, take `cactus_<version>_windows_amd64.zip` instead. The archive
names and the full platform table are in
[Development & releases](development.md#cli-binaries-on-a-release).

**From source.**

```sh
go build -o cactus ./cmd/cactus
go install github.com/FlameInTheDark/cactus-go/cmd/cactus@latest
```

## Command overview

```
NAME:
   cactus - on-device tool calling, speech and vision with the Cactus engine

USAGE:
   cactus [global options] [command [command options]]

COMMANDS:
   fetch       fetch the inference engine for this machine into the cache
   download    download model weights or a platform engine build
   run         answer one prompt (text or audio) through the library
   transcribe  transcribe a WAV clip with the Whistle speech model
   describe    describe an image with the Mirage vision model
   version     print the package and engine versions
   help        Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --help, -h     show help
   --version, -v  print the version
```

Conventions that apply to every command:

- Flags may appear before or after positional arguments
  (`cactus download linux-x86_64 --out dir` and
  `cactus download --out dir linux-x86_64` are equivalent).
- `--flag value` and `--flag=value` both parse.
- `cactus help <command>` prints the full reference for one command.
- Errors exit with status 1; usage mistakes exit with status 2.

- [`cactus fetch`](#fetch)
- [`cactus download`](#download)
- [`cactus run`](#run)
- [`cactus transcribe`](#transcribe)
- [`cactus describe`](#describe)
- [`cactus version`](#version)

## `fetch`

Downloads the engine build matching this machine from the Hugging Face Hub,
verifies it against the pinned release manifest, extracts the shared
library and caches it. Later agent runs find it there and never touch the
network.

```
cactus fetch [command options]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--out DIR` | the cache | Directory to place the engine. |
| `--platform-tag TAG` | current machine | Fetch the build for another device, e.g. `manylinux2014_aarch64` or `musllinux_1_2_x86_64`. |

Examples:

```sh
cactus fetch                                  # this machine, into the cache
cactus fetch --out /mnt/usb/engine            # this machine, elsewhere
cactus fetch --platform-tag win_amd64         # Windows build from a Linux host
```

Output includes the resolved path and a deployment hint for copying the file
to the same cache path on another device, or pointing `CACTUS_LIB_PATH` at
it. See [Engine management](engine.md#air-gapped--offline-devices) for the
full offline workflow.

## `download`

Pulls a model's base weights, a platform's standalone engine-runner files,
or a published `.cact` weights archive.

```
cactus download [command options] <needle3 | whistle | mirage | platform | org/repo[/file].cact>
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--out DIR` | `.` | Directory to place the files. |

A **model name** (`needle3`, `whistle`, `mirage`) pulls that model's base
weights into the cache — the same path an agent resolves on first use.

A **platform name** copies that platform's standalone runner files into
`--out/<platform>/` and marks the native `cactus` / `cactus.exe` runner
executable. Platforms: `macos-arm64`, `linux-x86_64`, `linux-arm64`,
`linux-armv7`, `linux-riscv64`, `linux-mipsel`, `windows-x86_64`,
`windows-arm64`, `android-arm64`, `android-armv7`, `android-riscv64`,
`ios-arm64`, `ios-sim-arm64`, `tvos-arm64`, `watchos-arm64`, `wasm`,
`wasm-component`.

A **repository spec** pulls a published `.cact` weights archive:
`org/repo/file.cact` names the file; `org/repo` works when the repository
holds exactly one archive.

Examples:

```sh
cactus download needle3                          # base text model weights
cactus download whistle                           # base speech model weights
cactus download linux-x86_64                      # standalone runner
cactus download Cactus-Compute/needle3/needle3.cact  # an explicit archive
cactus download myorg/my-tuned-tools              # the repo's single archive
```

The runner serves HTTP by itself: `./cactus --tools tools.json --serve`
(see [Engine management: standalone runner](engine.md#the-standalone-runner)).
Downloaded weights feed `needle.New(needle.WithWeights(...))`,
`whistle.New(whistle.WithWeights(...))` or `mirage.New(mirage.WithWeights(...))`
— see [Tuned weights](weights.md).

## `run`

Answers one prompt through the library itself, exercising the same engine
resolution, cache and download path your application will. With `--audio`
the prompt is a WAV clip: the engine transcribes it and turns the
transcript into tool calls in one pass.

```
cactus run [command options] [prompt ...]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--prompt TEXT` | — | Query text; positional words are joined by spaces when omitted. |
| `--audio FILE` | — | WAV clip to transcribe and answer instead of text. |
| `--language CODE` | detect | Language for `--audio` and `--keywords`, e.g. `en`. |
| `--keywords LIST` | — | Comma-separated words to favour during `--audio` transcription. |
| `--tools FILE` | — | Tools JSON file (`[{"name", "description", "parameters"}, ...]`). |
| `--system TEXT` | — | System facts, e.g. `date: 2026-07-21 Tue 14:30`. |
| `--weights FILE` | base model | Tuned `.cact` to run (worker process). |
| `--max N` | 512 | Response token limit. |
| `--json` | off | Print the raw response envelope as JSON. |
| `--timeout DURATION` | 5m | Overall time limit. |

Examples:

```sh
cactus run --tools tools.json --prompt "what's it like in Lagos right now?"
cactus run --tools tools.json what's it like in Lagos right now?
cactus run --tools tools.json --audio memo.wav --language en --keywords "Siobhan"
cactus run --tools tools.json --json --prompt "dim the study to 30"
cactus run --tools tools.json --weights my.cact --prompt "..."
cactus run --tools tools.json --system "date: 2026-09-11 Fri 10:30" --prompt "..."
```

Output, without `--json`:

```
heard      "what's it like in Lagos right now?"
call       get_weather
arguments  {
  "city": "Lagos"
}
confidence 0.9387
reasoning  'Lagos' -> city
```

A refusal prints `response   type=call (no call: off-topic, unsupported, or
negated)` — see [Responses: the refusal contract](responses.md#the-refusal-contract).

## `transcribe`

Speech to text with the Whistle model, on the same engine as Needle.

```
cactus transcribe [command options] <clip.wav>
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--language CODE` | detect | Force a language, e.g. `en`, `de`. |
| `--keywords LIST` | — | Comma-separated words and phrases to favour. |
| `--word-timestamps` | off | Print per-word start, end and probability. |
| `--weights FILE` | base model | Tuned `whistle.cact` to run. |
| `--json` | off | Print the raw transcript as JSON. |
| `--timeout DURATION` | 5m | Overall time limit. |

Supported languages: English, German, French, Spanish, Italian, Dutch and
Polish. One pass covers up to 30 s; longer clips are windowed by the
engine. Silence and steady noise return an empty text.

## `describe`

Image understanding with the Mirage model, on the same engine as Needle.

```
cactus describe [command options] <image.png>
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--sections LIST` | — | Comma-separated sections: `scene`, `objects`, `regions`, `text`, `relations`, `events`, `reasoning`. |
| `--fields LIST` | — | Comma-separated per-object fields: `bbox`, `score`, `point`, `position`, `description`, `count`, `mask`. |
| `--conf N` | engine default | Detection confidence threshold (0-1). |
| `--form FILE` | — | Form JSON file: ordered fields with `enum` / integer bounds / `string`. |
| `--language CODE` | engine default | Output language, e.g. `en`. |
| `--keywords LIST` | — | Comma-separated words and phrases to favour. |
| `--weights FILE` | base model | Tuned `mirage.cact` to run. |
| `--json` | off | Print the raw result as JSON. |
| `--timeout DURATION` | 5m | Overall time limit. |

Without `--form` the command prints a structured description; with it, the
filled form. Input can be PNG, JPEG or GIF (JPEGs are auto-rotated by
their EXIF orientation).

## `version`

```sh
cactus version
# cactus-go 0.5.0 (engine 4.1.1)
```

Reports the library version and the pinned engine build it targets.

## Where to go next

- What `fetch` puts where: [Engine management](engine.md)
- What to do with downloaded weights: [Tuned weights](weights.md)
- The response envelope `run --json` prints: [Responses](responses.md)
