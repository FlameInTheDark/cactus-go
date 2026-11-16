// Command cactus is the companion CLI of the cactus-go library: it fetches
// and caches the shared Cactus inference engine, downloads model weights
// and platform builds from the Hugging Face Hub, and runs one-shot text,
// speech and vision queries.
//
//	cactus fetch                      engine for this machine into the cache
//	cactus download whistle           a model's base weights
//	cactus download linux-x86_64      a platform's standalone engine runner
//	cactus run --prompt "..."         one completion through the library
//	cactus transcribe memo.wav        speech to text with Whistle
//	cactus describe photo.png         image understanding with Mirage
//
// The CLI is built with urfave/cli v3; run "cactus help <command>" for the
// full flag reference of any subcommand.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/FlameInTheDark/cactus-go"
	"github.com/FlameInTheDark/cactus-go/fetch"
	"github.com/FlameInTheDark/cactus-go/mirage"
	"github.com/FlameInTheDark/cactus-go/needle"
	"github.com/FlameInTheDark/cactus-go/whistle"
)

func main() {
	cmd := &cli.Command{
		Name:    "cactus",
		Usage:   "on-device tool calling, speech and vision with the Cactus engine",
		Version: cactus.Version,
		Description: "Fetches and caches the shared Cactus inference engine, downloads model " +
			"weights and standalone platform builds from the Hugging Face Hub, and answers " +
			"text, speech and image prompts through the library.",
		Commands: []*cli.Command{
			fetchCommand(), downloadCommand(), runCommand(),
			transcribeCommand(), describeCommand(), versionCommand(),
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "cactus: %v\n", err)
		os.Exit(1)
	}
}

// fetchCommand downloads the engine library for this machine (or an
// explicit --platform-tag) into the cache.
func fetchCommand() *cli.Command {
	return &cli.Command{
		Name:  "fetch",
		Usage: "fetch the inference engine for this machine into the cache",
		UsageText: "cactus fetch [command options]\n\n" +
			"Downloads the engine build matching this machine from the Hugging Face Hub,\n" +
			"verifies it against the pinned release manifest, extracts the shared library\n" +
			"and caches it. Later agent runs find it there and never touch the network.",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "out",
				Usage: "directory to place the engine (default: the cache)",
			},
			&cli.StringFlag{
				Name:  "platform-tag",
				Usage: "fetch the build for another device, e.g. manylinux2014_aarch64",
			},
		},
		Action: fetchAction,
	}
}

func fetchAction(ctx context.Context, cmd *cli.Command) error {
	dest := cmd.String("out")
	if dest == "" {
		cache, err := fetch.EngineCacheDir()
		if err != nil {
			return err
		}
		dest = cache
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	fetch.ProgressHook = newProgressPrinter()
	path, err := fetch.FetchLibrary(ctx, cmd.String("platform-tag"), dest)
	if err != nil {
		return err
	}
	fmt.Printf("  %-9s %s\n", "engine", path)
	fmt.Printf("  %-9s copy to ~/.cache/cactus/engine/%s/%s/ on the device, or point CACTUS_LIB_PATH at the file\n",
		"deploy", fetch.EngineVersion, fetch.EngineRevision)
	return nil
}

// downloadCommand pulls base model weights, a platform's standalone
// engine-runner files, or a published .cact weights archive.
func downloadCommand() *cli.Command {
	return &cli.Command{
		Name:  "download",
		Usage: "download model weights or a platform engine build",
		UsageText: "cactus download [command options] <needle3 | whistle | mirage | platform | org/repo[/file].cact>\n\n" +
			"A model name pulls that model's base weights into the cache (the same path an\n" +
			"agent resolves on first use). A platform name (linux-x86_64, windows-x86_64,\n" +
			"macos-arm64, wasm, ...) copies that platform's standalone engine runner into\n" +
			"--out/<platform>/. A repository spec (org/repo or org/repo/file.cact) pulls a\n" +
			"published weights archive.",
		ArgsUsage: "<needle3 | whistle | mirage | platform | org/repo[/file].cact>",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "out",
				Value: ".",
				Usage: "directory to place the files",
			},
		},
		Action: downloadAction,
	}
}

func downloadAction(ctx context.Context, cmd *cli.Command) error {
	if !cmd.Args().Present() {
		return cli.Exit("download needs one argument: a model name, a platform name or <org>/<repo>[/<file>.cact]", 2)
	}
	spec := cmd.Args().First()
	fetch.ProgressHook = newProgressPrinter()

	// Base model weights: needle3, whistle, mirage.
	if fetch.ValidModel(spec) {
		path, err := fetch.WeightsPath(ctx, spec)
		if err != nil {
			return err
		}
		fmt.Printf("  %-9s %s  %.2f MB\n", "weights", path, fileSizeMB(path))
		switch spec {
		case fetch.Needle3:
			fmt.Printf("  %-9s needle.New(needle.WithWeights(%q), needle.WithTools(...))\n", "next", path)
		case fetch.Whistle:
			fmt.Printf("  %-9s whistle.New(whistle.WithWeights(%q))\n", "next", path)
		case fetch.Mirage:
			fmt.Printf("  %-9s mirage.New(mirage.WithWeights(%q))\n", "next", path)
		}
		return nil
	}

	if !strings.Contains(spec, "/") {
		paths, err := fetch.DownloadPlatform(ctx, spec, cmd.String("out"))
		if err != nil {
			return err
		}
		for _, p := range paths {
			fmt.Printf("  %-9s %s  %.2f MB\n", "file", p, fileSizeMB(p))
			if base := filepath.Base(p); base == "cactus" || base == "cactus.exe" {
				fmt.Printf("  %-9s %s\n", "runner", p)
			}
		}
		return nil
	}

	path, err := fetch.DownloadWeights(ctx, spec, cmd.String("out"))
	if err != nil {
		return err
	}
	fmt.Printf("  %-9s %s  %.2f MB\n", "weights", path, fileSizeMB(path))
	fmt.Printf("  %-9s needle.New(needle.WithWeights(%q), needle.WithTools(...))\n", "next", path)
	return nil
}

// runCommand answers one prompt through the library, exercising the same
// engine resolution, cache and download path an application would.
func runCommand() *cli.Command {
	return &cli.Command{
		Name:  "run",
		Usage: "answer one prompt (text or audio) through the library",
		UsageText: "cactus run [command options] [prompt ...]\n\n" +
			"The prompt comes from --prompt or the positional arguments joined by spaces;\n" +
			"with --audio the prompt is a WAV clip transcribed and turned into tool calls\n" +
			"in one pass. With --tools the model may produce tool calls; without tools\n" +
			"every prompt is off-topic and the engine refuses. Prints the requested call,\n" +
			"its arguments and the confidence, or the raw response envelope with --json.",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "prompt",
				Usage: "query text for tool-call generation",
			},
			&cli.StringFlag{
				Name:  "audio",
				Usage: "WAV file to transcribe and answer instead of text",
			},
			&cli.StringFlag{
				Name:  "language",
				Usage: "language code for --audio, e.g. en (default: detect)",
			},
			&cli.StringFlag{
				Name:  "keywords",
				Usage: "comma-separated words to favour during --audio transcription",
			},
			&cli.StringFlag{
				Name:  "tools",
				Usage: "tools JSON file for tool-call generation",
			},
			&cli.StringFlag{
				Name:  "system",
				Usage: "system facts, e.g. 'date: 2026-07-21 Tue 14:30'",
			},
			&cli.StringFlag{
				Name:  "weights",
				Usage: "tuned .cact to run (default: the base model)",
			},
			&cli.IntFlag{
				Name:  "max",
				Value: 512,
				Usage: "response token limit",
			},
			&cli.BoolFlag{
				Name:  "json",
				Usage: "print the raw response envelope as JSON",
			},
			&cli.DurationFlag{
				Name:  "timeout",
				Value: 5 * time.Minute,
				Usage: "overall time limit",
			},
		},
		Action: runAction,
	}
}

func runAction(ctx context.Context, cmd *cli.Command) error {
	runCtx, cancel := context.WithTimeout(ctx, cmd.Duration("timeout"))
	defer cancel()

	var toolsOpt needle.Option = needle.WithTools()
	if toolsPath := cmd.String("tools"); toolsPath != "" {
		data, err := os.ReadFile(toolsPath)
		if err != nil {
			return err
		}
		toolsOpt = needle.WithToolsJSON(string(data))
	}
	opts := []needle.Option{toolsOpt}
	if system := cmd.String("system"); system != "" {
		opts = append(opts, needle.WithSystem(system))
	}
	if weights := cmd.String("weights"); weights != "" {
		opts = append(opts, needle.WithWeights(weights))
	}

	agent, err := needle.New(opts...)
	if err != nil {
		return err
	}
	defer agent.Close()
	fetch.ProgressHook = newProgressPrinter()

	var resp *needle.Response
	if audioPath := cmd.String("audio"); audioPath != "" {
		completeOpts := []needle.CompleteOption{needle.MaxTokens(cmd.Int("max"))}
		if lang := cmd.String("language"); lang != "" {
			completeOpts = append(completeOpts, needle.WithLanguage(lang))
		}
		if kw := cmd.String("keywords"); kw != "" {
			completeOpts = append(completeOpts, needle.WithKeywords(splitKeywords(kw)...))
		}
		resp, err = agent.CompleteAudio(runCtx, audioPath, completeOpts...)
	} else {
		query := cmd.String("prompt")
		if query == "" && cmd.Args().Present() {
			query = strings.Join(cmd.Args().Slice(), " ")
		}
		resp, err = agent.Complete(runCtx, query, needle.MaxTokens(cmd.Int("max")))
	}
	if err != nil {
		return err
	}
	if cmd.Bool("json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}
	if resp.AudioText != nil && *resp.AudioText != "" {
		fmt.Printf("heard      %s\n", *resp.AudioText)
	}
	if call := resp.FirstCall(); call != nil {
		pretty, _ := json.MarshalIndent(call.Arguments, "", "  ")
		fmt.Printf("call       %s\narguments  %s\n", call.Name, string(pretty))
	} else {
		fmt.Printf("response   type=%s (no call: off-topic, unsupported, or negated)\n", resp.Type)
	}
	if resp.Confidence != nil {
		fmt.Printf("confidence %.4f\n", *resp.Confidence)
	}
	if resp.Reasoning != nil && *resp.Reasoning != "" {
		fmt.Printf("reasoning  %s\n", *resp.Reasoning)
	}
	return nil
}

// transcribeCommand runs one clip through Whistle.
func transcribeCommand() *cli.Command {
	return &cli.Command{
		Name:  "transcribe",
		Usage: "transcribe a WAV clip with the Whistle speech model",
		UsageText: "cactus transcribe [command options] <clip.wav>\n\n" +
			"Speech to text in English, German, French, Spanish, Italian, Dutch and\n" +
			"Polish, on the same engine as Needle. One pass covers up to 30 s; longer\n" +
			"clips are windowed by the engine.",
		ArgsUsage: "<clip.wav>",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "language",
				Usage: "language code, e.g. en (default: detect)",
			},
			&cli.StringFlag{
				Name:  "keywords",
				Usage: "comma-separated words and phrases to favour",
			},
			&cli.BoolFlag{
				Name:  "word-timestamps",
				Usage: "print per-word start, end and probability",
			},
			&cli.StringFlag{
				Name:  "weights",
				Usage: "tuned whistle .cact to run (default: the base model)",
			},
			&cli.BoolFlag{
				Name:  "json",
				Usage: "print the raw transcript as JSON",
			},
			&cli.DurationFlag{
				Name:  "timeout",
				Value: 5 * time.Minute,
				Usage: "overall time limit",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if !cmd.Args().Present() {
				return cli.Exit("transcribe needs a WAV file path", 2)
			}
			runCtx, cancel := context.WithTimeout(ctx, cmd.Duration("timeout"))
			defer cancel()
			fetch.ProgressHook = newProgressPrinter()

			model := whistle.New()
			if weights := cmd.String("weights"); weights != "" {
				model = whistle.New(whistle.WithWeights(weights))
			}
			opts := []whistle.TranscribeOption{}
			if lang := cmd.String("language"); lang != "" {
				opts = append(opts, whistle.WithLanguage(lang))
			}
			if kw := cmd.String("keywords"); kw != "" {
				opts = append(opts, whistle.WithKeywords(splitKeywords(kw)...))
			}
			if cmd.Bool("word-timestamps") {
				opts = append(opts, whistle.WordTimestamps(true))
			}
			result, err := model.Transcribe(runCtx, cmd.Args().First(), opts...)
			if err != nil {
				return err
			}
			if cmd.Bool("json") {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}
			fmt.Println(result.Text)
			for _, word := range result.Words {
				fmt.Printf("  %6.2f - %6.2f  %s (%.2f)\n", word.Start, word.End, word.Word, word.Probability)
			}
			fmt.Printf("  language %s   ttft %.0f ms   decode %.0f tok/s\n",
				result.Language, result.TTFTMS, result.DecodeTPS)
			return nil
		},
	}
}

// describeCommand runs one image through Mirage.
func describeCommand() *cli.Command {
	return &cli.Command{
		Name:  "describe",
		Usage: "describe an image with the Mirage vision model",
		UsageText: "cactus describe [command options] <image.png>\n\n" +
			"Image understanding, OCR and object detection on the same engine as Needle.\n" +
			"--sections and --fields select what comes back; --form fills an ordered\n" +
			"form instead of a free description.",
		ArgsUsage: "<image.png>",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "sections",
				Usage: "comma-separated sections: scene, objects, regions, text, relations, events, reasoning",
			},
			&cli.StringFlag{
				Name:  "fields",
				Usage: "comma-separated per-object fields: bbox, score, point, position, description, count, mask",
			},
			&cli.StringFlag{
				Name:  "conf",
				Usage: "detection confidence threshold (0-1)",
			},
			&cli.StringFlag{
				Name:  "form",
				Usage: "form JSON file: ordered fields with enum / integer bounds / string",
			},
			&cli.StringFlag{
				Name:  "language",
				Usage: "output language code, e.g. en",
			},
			&cli.StringFlag{
				Name:  "keywords",
				Usage: "comma-separated words and phrases to favour",
			},
			&cli.StringFlag{
				Name:  "weights",
				Usage: "tuned mirage .cact to run (default: the base model)",
			},
			&cli.BoolFlag{
				Name:  "json",
				Usage: "print the raw result as JSON (default)",
			},
			&cli.DurationFlag{
				Name:  "timeout",
				Value: 5 * time.Minute,
				Usage: "overall time limit",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if !cmd.Args().Present() {
				return cli.Exit("describe needs an image file path", 2)
			}
			runCtx, cancel := context.WithTimeout(ctx, cmd.Duration("timeout"))
			defer cancel()
			fetch.ProgressHook = newProgressPrinter()

			model := mirage.New()
			if weights := cmd.String("weights"); weights != "" {
				model = mirage.New(mirage.WithWeights(weights))
			}
			opts := []mirage.DescribeOption{}
			if sections := cmd.String("sections"); sections != "" {
				opts = append(opts, mirage.WithSections(strings.Split(sections, ",")...))
			}
			if fields := cmd.String("fields"); fields != "" {
				opts = append(opts, mirage.WithFields(strings.Split(fields, ",")...))
			}
			if conf := cmd.String("conf"); conf != "" {
				var value float64
				if _, err := fmt.Sscanf(conf, "%g", &value); err != nil {
					return cli.Exit("--conf needs a number between 0 and 1", 2)
				}
				opts = append(opts, mirage.WithConf(value))
			}
			if lang := cmd.String("language"); lang != "" {
				opts = append(opts, mirage.WithLanguage(lang))
			}
			if kw := cmd.String("keywords"); kw != "" {
				opts = append(opts, mirage.WithKeywords(splitKeywords(kw)...))
			}
			var result map[string]any
			var err error
			if formPath := cmd.String("form"); formPath != "" {
				formRaw, readErr := os.ReadFile(formPath)
				if readErr != nil {
					return readErr
				}
				var form map[string]any
				if jsonErr := json.Unmarshal(formRaw, &form); jsonErr != nil {
					return fmt.Errorf("--form must be a JSON object of fields: %w", jsonErr)
				}
				result, err = model.Extract(runCtx, cmd.Args().First(), form, opts...)
			} else {
				result, err = model.Describe(runCtx, cmd.Args().First(), opts...)
			}
			if err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		},
	}
}

// versionCommand prints the library version and the engine build version.
func versionCommand() *cli.Command {
	return &cli.Command{
		Name:  "version",
		Usage: "print the package and engine versions",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			fmt.Printf("cactus-go %s (Cactus engine %s)\n", cactus.Version, fetch.EngineVersion)
			fmt.Printf("models: needle3, whistle, mirage\n")
			return nil
		},
	}
}

// splitKeywords turns a comma-separated flag value into phrases.
func splitKeywords(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// progressPrinter throttles download progress to one line per percent so a
// 35 MB engine fetch does not flood the terminal.
type progressPrinter struct{ lastPct int64 }

func newProgressPrinter() func(label string, total, done int64) {
	p := &progressPrinter{}
	return p.print
}

func (p *progressPrinter) print(label string, total, done int64) {
	if total <= 0 {
		fmt.Fprintf(os.Stderr, "\r  %-9s %s  %6.1f MB", "fetch", label, float64(done)/1e6)
		return
	}
	pct := 100 * done / total
	if pct < p.lastPct {
		p.lastPct = -1 // a new file started
	}
	if pct == p.lastPct && done < total {
		return
	}
	p.lastPct = pct
	fmt.Fprintf(os.Stderr, "\r  %-9s %s  %6.1f / %.1f MB (%3d%%)", "fetch", label,
		float64(done)/1e6, float64(total)/1e6, int(pct))
	if done >= total {
		fmt.Fprintln(os.Stderr)
		p.lastPct = -1
	}
}

func fileSizeMB(path string) float64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return float64(st.Size()) / 1e6
}
