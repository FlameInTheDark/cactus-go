// Package whistle is the speech-to-text model of the Cactus stack:
// transcription, word timestamps, live streaming and speech embeddings in
// English, German, French, Spanish, Italian, Dutch and Polish, running on
// the same shared engine and the same install as Needle.
//
//	whistleModel, err := whistle.New()
//	result, err := whistleModel.Transcribe(ctx, "memo.wav",
//		whistle.WithLanguage("en"), whistle.WordTimestamps(true))
//	fmt.Println(result.Text, result.Words)
//
// Audio is a WAV file path or 16 kHz mono float32 samples in [-1, 1], of
// any length; one pass covers up to 30 seconds and longer clips are
// windowed by the engine. Silence and steady noise return an empty text
// and language; music can still come out as text, so put a voice activity
// detector or wake word in front of an always-on microphone.
package whistle

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"iter"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/FlameInTheDark/cactus-go/audio"
	"github.com/FlameInTheDark/cactus-go/fetch"
	"github.com/FlameInTheDark/cactus-go/internal/engine"
)

// Languages is the list of languages the shipped model transcribes.
var Languages = []string{"en", "de", "fr", "es", "it", "nl", "pl"}

// outputBytesPerSecond sizes the JSON output buffer per second of audio.
const outputBytesPerSecond = 4096

// defaultBufferSize is the initial JSON output buffer.
const defaultBufferSize = 1 << 18

// Word is one transcribed word with its timing and probability.
type Word struct {
	Word        string  `json:"word"`
	Start       float64 `json:"start"`
	End         float64 `json:"end"`
	Probability float64 `json:"probability"`
}

// Transcription is the result of one transcription pass: the text, the
// detected (or forced) language, the time to first token and the decode
// throughput, plus per-word timings when WordTimestamps was set. Silence
// yields empty text and language with zero timings.
type Transcription struct {
	Text      string  `json:"text"`
	Language  string  `json:"language"`
	TTFTMS    float64 `json:"ttft_ms"`
	DecodeTPS float64 `json:"decode_tps"`
	Words     []Word  `json:"words"`
}

// StreamStep is one live-transcription update: the text and words this
// chunk committed (times from the start of the stream), the unconfirmed
// pending tail, the language, the seconds of audio received so far and
// the milliseconds this pass took. Join the texts with a space.
type StreamStep struct {
	Text     string  `json:"text"`
	Words    []Word  `json:"words"`
	Pending  string  `json:"pending"`
	Language string  `json:"language"`
	Received float64 `json:"received"`
	PassMS   float64 `json:"pass_ms"`
}

// Whistle is a speech-to-text model instance bound to one weights archive
// (the shipped whistle.cact by default). Calls share the engine runtime
// and are serialized across threads. The engine and weights are located -
// and downloaded on first use - lazily, with the context of the first
// call.
type Whistle struct {
	mu         sync.Mutex
	weights    string
	enginePath string
	bufSize    int
	logger     *slog.Logger

	lib *engine.Library
	buf []byte
}

// Option configures a Whistle.
type Option func(*config)

type config struct {
	weights    string
	enginePath string
	bufSize    int
	logger     *slog.Logger
}

// WithWeights runs the model on a tuned .cact archive instead of the
// shipped whistle.cact.
func WithWeights(path string) Option {
	return func(c *config) { c.weights = path }
}

// WithEnginePath pins the native engine library to an explicit path.
func WithEnginePath(path string) Option {
	return func(c *config) { c.enginePath = path }
}

// WithBufferSize sets the initial JSON output buffer in bytes (default
// 262144). The buffer grows with the audio automatically.
func WithBufferSize(size int) Option {
	return func(c *config) { c.bufSize = size }
}

// WithLogger installs a logger for download and engine lifecycle events.
func WithLogger(logger *slog.Logger) Option {
	return func(c *config) { c.logger = logger }
}

// New creates a Whistle model. Pass WithWeights to run a tuned archive.
func New(opts ...Option) *Whistle {
	cfg := config{bufSize: defaultBufferSize}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return &Whistle{
		weights:    cfg.weights,
		enginePath: cfg.enginePath,
		bufSize:    cfg.bufSize,
		logger:     cfg.logger,
	}
}

// Weights reports the weights archive the model runs on (resolved after
// the first call; empty when a custom path was not set and no call has
// run yet).
func (w *Whistle) Weights() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.weights
}

// bindLocked resolves the engine and weights; w.mu must be held.
func (w *Whistle) bindLocked(ctx context.Context) error {
	if w.lib != nil && w.weights != "" && engine.WeightsLoaded(w.weights) {
		return nil
	}
	libPath := w.enginePath
	if libPath == "" {
		var err error
		libPath, err = fetch.LibraryPath(ctx)
		if err != nil {
			return err
		}
	}
	lib, err := engine.LoadNative(libPath)
	if err != nil {
		return err
	}
	weights := w.weights
	if weights == "" {
		weights, err = fetch.WeightsPath(ctx, fetch.Whistle)
		if err != nil {
			return err
		}
		w.weights = weights
	}
	if err := lib.EnsureWeights(weights); err != nil {
		return err
	}
	if w.buf == nil {
		size := w.bufSize
		if size <= 0 {
			size = defaultBufferSize
		}
		w.buf = make([]byte, size)
	}
	w.lib = lib
	return nil
}

// reserve grows the output buffer to fit a transcript of the given
// sample count; w.mu (or the stream lock) must be held.
func (w *Whistle) reserve(sampleCount int) {
	need := sampleCount / audio.SampleRate * outputBytesPerSecond
	if need > len(w.buf) {
		w.buf = make([]byte, need)
	}
}

// TranscribeOption tunes one transcription.
type TranscribeOption func(*transcribeConfig)

type transcribeConfig struct {
	language       string
	keywords       any
	wordTimestamps bool
}

// WithLanguage forces the transcription language, as an ISO code ("en",
// "de", ...). Empty detects it.
func WithLanguage(language string) TranscribeOption {
	return func(c *transcribeConfig) { c.language = language }
}

// WithKeywords biases transcription towards the given words and phrases
// (one string with one phrase per line, or the phrases as arguments).
func WithKeywords(keywords ...string) TranscribeOption {
	return func(c *transcribeConfig) { c.keywords = keywords }
}

// WordTimestamps asks for per-word timings.
func WordTimestamps(wordTimestamps bool) TranscribeOption {
	return func(c *transcribeConfig) { c.wordTimestamps = wordTimestamps }
}

// Transcribe converts one clip to text. input is a WAV path or 16 kHz
// mono samples (a []float32, []float64 or raw float32 LE []byte); WAV
// files of any other rate are resampled. One pass covers up to 30 s and
// longer clips are windowed by the engine.
func (w *Whistle) Transcribe(ctx context.Context, input any, opts ...TranscribeOption) (*Transcription, error) {
	cfg := transcribeConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.bindLocked(ctx); err != nil {
		return nil, err
	}
	samples, err := audio.Samples(input)
	if err != nil {
		return nil, err
	}
	language, keywords, err := audio.Options(cfg.language, cfg.keywords)
	if err != nil {
		return nil, err
	}
	w.reserve(len(samples))
	raw, err := w.lib.Transcribe(samples, language, keywords, cfg.wordTimestamps, w.buf)
	if err != nil {
		return nil, err
	}
	var out Transcription
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("whistle: engine returned an unparseable transcript (%v)", err)
	}
	return &out, nil
}

// Stream runs live transcription with no limit on length. chunks yields
// buffers of 16 kHz mono samples, about a second each; every chunk
// re-transcribes the last seconds and commits what two passes agree on.
// The returned sequence yields one step per chunk, then one for the tail
// when the chunks end. Breaking out of the loop early stops the stream
// cleanly. The engine lock is held for the whole stream, so no other
// engine call interleaves.
func (w *Whistle) Stream(ctx context.Context, chunks iter.Seq[[]float32], opts ...TranscribeOption) iter.Seq2[*StreamStep, error] {
	return func(yield func(*StreamStep, error) bool) {
		cfg := transcribeConfig{}
		for _, opt := range opts {
			if opt != nil {
				opt(&cfg)
			}
		}
		w.mu.Lock()
		if err := w.bindLocked(ctx); err != nil {
			w.mu.Unlock()
			yield(nil, err)
			return
		}
		language, keywords, err := audio.Options(cfg.language, cfg.keywords)
		if err != nil {
			w.mu.Unlock()
			yield(nil, err)
			return
		}
		lib, buf := w.lib, w.buf
		w.mu.Unlock()

		// Hold the shared engine lock for the whole stream, mirroring the
		// Python generator's `with lock`. Deferred stop guarantees the
		// engine's stream state is flushed even on error or early break.
		engine.WithLock(func() {
			stopped := false
			stop := func() {
				if !stopped {
					stopped = true
					_, _ = lib.StreamStop(buf)
				}
			}
			defer stop()
			for chunk := range chunks {
				if err := ctx.Err(); err != nil {
					yield(nil, err)
					return
				}
				w.mu.Lock()
				w.reserve(len(chunk))
				buf = w.buf
				w.mu.Unlock()
				raw, err := lib.StreamProcess(chunk, language, keywords, buf)
				if err != nil {
					yield(nil, err)
					return
				}
				step, err := parseStep(raw)
				if err != nil {
					yield(nil, err)
					return
				}
				if !yield(step, nil) {
					return // consumer stopped early; stop() flushes
				}
			}
			raw, err := lib.StreamStop(buf)
			stopped = true
			if err != nil {
				yield(nil, err)
				return
			}
			step, err := parseStep(raw)
			if err != nil {
				yield(nil, err)
				return
			}
			yield(step, nil)
		})
	}
}

func parseStep(raw string) (*StreamStep, error) {
	var step StreamStep
	if err := json.Unmarshal([]byte(raw), &step); err != nil {
		return nil, fmt.Errorf("whistle: engine returned an unparseable stream step (%v)", err)
	}
	return &step, nil
}

// Embed returns the speech encoder output for one clip: one row of
// floats per 80 ms frame, flattened. Clips longer than 30 s are rejected
// by the engine.
func (w *Whistle) Embed(ctx context.Context, input any) ([]float32, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.bindLocked(ctx); err != nil {
		return nil, err
	}
	samples, err := audio.Samples(input)
	if err != nil {
		return nil, err
	}
	return w.lib.EmbedAudio(samples)
}

// LanguagesOf returns the languages the weights archive transcribes, read
// from the tokenizer's special <|xx|> tokens. The shipped archive
// reports Languages; a broken or foreign file falls back to it.
func LanguagesOf(weights string) []string {
	raw, err := os.ReadFile(weights)
	if err != nil || len(raw) < 12 {
		return Languages
	}
	tag := binary.LittleEndian.Uint32(raw[0:4])
	count := int(binary.LittleEndian.Uint32(raw[4:8]))
	codebook := int(binary.LittleEndian.Uint32(raw[8:12]))
	if tag != engine.TagNeedle3 {
		return Languages
	}
	record := engine.CactHeaderSize + codebook*4
	if count > 1<<20 {
		return Languages
	}
	for i := 0; i < count; i++ {
		if record < 0 || record+44 > len(raw) {
			return Languages
		}
		if raw[record] != engine.DtypeRaw {
			record += 44
			continue
		}
		offset := int(binary.LittleEndian.Uint64(raw[record+20 : record+28]))
		size := int(binary.LittleEndian.Uint64(raw[record+28 : record+36]))
		if offset < 0 || size < 0 || offset+size > len(raw) {
			return Languages
		}
		return tokenizerLanguages(raw[offset : offset+size])
	}
	return Languages
}

// tokenizerLanguages walks the tokenizer blob's special-token table and
// collects the language pieces ("<|en|>" -> "en").
func tokenizerLanguages(blob []byte) []string {
	found := []string{}
	if len(blob) < 28 {
		return Languages
	}
	count := int(binary.LittleEndian.Uint32(blob[0:4]))
	at := 24
	for i := 0; i < count; i++ {
		if at+7 > len(blob) {
			break
		}
		length := int(binary.LittleEndian.Uint16(blob[at+5 : at+7]))
		if length < 0 || at+7+length > len(blob) {
			break
		}
		piece := string(blob[at+7 : at+7+length])
		at += 7 + length
		if strings.HasPrefix(piece, "<|") && strings.HasSuffix(piece, "|>") {
			found = append(found, strings.TrimSuffix(strings.TrimPrefix(piece, "<|"), "|>"))
		}
	}
	if len(found) == 0 {
		return Languages
	}
	return found
}

// The shared default model: package-level calls reuse one instance per
// weights path, so repeated Transcribe calls skip re-binding.
var (
	sharedMu    sync.Mutex
	sharedModel = map[string]*Whistle{}
)

func shared(weights string) *Whistle {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if _, ok := sharedModel[weights]; !ok {
		opts := []Option{}
		if weights != "" {
			opts = append(opts, WithWeights(weights))
		}
		sharedModel[weights] = New(opts...)
	}
	return sharedModel[weights]
}

// Transcribe converts one clip to text on the model this process already
// has loaded. See (*Whistle).Transcribe.
func Transcribe(ctx context.Context, input any, opts ...TranscribeOption) (*Transcription, error) {
	return shared("").Transcribe(ctx, input, opts...)
}

// Stream runs live transcription on the model this process already has
// loaded. See (*Whistle).Stream.
func Stream(ctx context.Context, chunks iter.Seq[[]float32], opts ...TranscribeOption) iter.Seq2[*StreamStep, error] {
	return shared("").Stream(ctx, chunks, opts...)
}

// Embed returns the speech encoder output on the model this process
// already has loaded. See (*Whistle).Embed.
func Embed(ctx context.Context, input any) ([]float32, error) {
	return shared("").Embed(ctx, input)
}

// TranscribeWith runs one clip against an explicit tuned archive.
func TranscribeWith(ctx context.Context, weights string, input any, opts ...TranscribeOption) (*Transcription, error) {
	return shared(weights).Transcribe(ctx, input, opts...)
}

// StreamWith runs live transcription against an explicit tuned archive.
func StreamWith(ctx context.Context, weights string, chunks iter.Seq[[]float32], opts ...TranscribeOption) iter.Seq2[*StreamStep, error] {
	return shared(weights).Stream(ctx, chunks, opts...)
}
