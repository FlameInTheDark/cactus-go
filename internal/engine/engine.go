// Package engine contains the low-level bindings to the shared Cactus
// inference engine (libcactus.so / libcactus.dll / libcactus.dylib).
//
// One engine runs every Cactus model: Needle 3 (text, tool calling),
// Whistle (speech) and Mirage (vision) are separate .cact weight archives
// loaded into the same library. The C ABI (see the published cactus.h) is:
//
//	int  cactus_load(const unsigned char* cact, unsigned long long n);
//	int  cactus_models(void);                 // TEXT=1 | SPEECH=2 | VISION=4
//	const char* cactus_last_error(void);
//	int  cactus_init(const char* system_prompt,
//	                 const char* tools_json,
//	                 const char* tool_index_path);
//	int  cactus_complete(const char* input,
//	                     const float* pcm, int samples,
//	                     int max_new_tokens,
//	                     char* out, int out_capacity);
//	void cactus_set_audio(const char* language,
//	                      const char* keywords, int word_timestamps);
//	void cactus_set_threads(int threads);
//	void cactus_reset(void);
//	int  cactus_transcribe(const float* pcm, int samples,
//	                       const char* language, const char* keywords,
//	                       int word_timestamps,
//	                       char* out, int out_capacity);
//	int  cactus_stream_transcribe_process(const float* pcm, int samples,
//	                                      const char* language,
//	                                      const char* keywords,
//	                                      char* out, int out_capacity);
//	int  cactus_stream_transcribe_stop(char* out, int out_capacity);
//	int  cactus_describe(const unsigned char* rgb, int width, int height,
//	                     const char* options,
//	                     const char* language, const char* keywords,
//	                     char* out, int out_capacity);
//	int  cactus_embed(const char* input, const float* pcm, int samples,
//	                  float* out, int out_capacity);
//
// All functions return a negative code on failure; cactus_complete,
// cactus_init and cactus_embed return counts (generated tokens, prompt
// prefix tokens and embedding dimensions respectively) on success. Callers
// serialize every call through the shared runtime: the engine keeps global
// state.
//
// Two backends implement the text contract:
//
//   - TextSession: the shared library is loaded into the current process
//     with purego (dlopen on Unix, LoadLibrary on Windows; no cgo
//     anywhere) and the symbols are called directly. This is the path used
//     for the base model whose weights are loaded once per process.
//   - Worker: a child process - a re-exec of the host binary - loads the
//     library, injects a tuned .cact weights archive with cactus_load, and
//     serves length-prefixed JSON requests over stdin/stdout. The native
//     engine keeps a global text session, so each tuned agent gets its own
//     process.
package engine

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// Model bits reported by cactus_models().
const (
	ModelText   = 1
	ModelSpeech = 2
	ModelVision = 4
)

// .cact archive format tags (little-endian uint32 at offset 0).
const (
	TagNeedle2 = 0x05E12A83
	TagNeedle3 = 0x05E12A84
)

// Backend is a running text engine session bound to one toolset.
type Backend interface {
	// Complete feeds one text turn to the engine and returns the raw
	// JSON response envelope written by the engine.
	Complete(ctx context.Context, input string, maxNewTokens int) (string, error)
	// Reset rewinds the conversation, keeping the tools loaded.
	Reset() error
	// Close releases resources held by the backend. It is safe to call
	// more than once.
	Close() error
	// PrefixTokens reports the tool-prompt prefix token count observed
	// when the session was last initialized.
	PrefixTokens() int
}

// TextBackend is a Backend that also accepts audio turns and embedding
// requests. Both shipped backends implement it.
type TextBackend interface {
	Backend
	// CompleteAudio runs one turn driven by speech: the transcript feeds
	// the same tool-calling grammar and the envelope carries audio_text.
	CompleteAudio(ctx context.Context, samples []float32, opts AudioOptions, maxNewTokens int) (string, error)
	// Embed returns the text embedding of the input.
	Embed(ctx context.Context, text string) ([]float32, error)
}

// AudioOptions configures one audio-driven completion.
type AudioOptions struct {
	// Language is an ISO code ("en", "de", ...) or empty to detect it.
	Language string
	// Keywords is a newline-separated string of words and phrases to
	// favour during transcription.
	Keywords string
	// WordTimestamps asks the engine for per-word timings.
	WordTimestamps bool
	// SpeechWeights is the resolved whistle.cact path the backend should
	// run; the in-process backend loads it on demand, the worker child
	// loads it when it differs from the archive it already holds.
	SpeechWeights string
}

// GenerationOfCact reports the engine generation of a .cact archive held
// in memory. Needle 2 archives are recognized but refused: this build
// targets the Cactus 4 engine generation.
func GenerationOfCact(data []byte) (int, error) {
	if len(data) < 4 {
		return 0, fmt.Errorf("not a complete .cact archive")
	}
	tag := binary.LittleEndian.Uint32(data[:4])
	if tag == TagNeedle3 {
		return 3, nil
	}
	if tag == TagNeedle2 {
		return 0, fmt.Errorf("this is a Needle 2 archive (tag 0x%08x); Needle 2 needs the older engine - "+
			"use cactus-go v0.3 or a Needle 3 / Whistle / Mirage archive", tag)
	}
	return 0, fmt.Errorf("unknown .cact format tag 0x%08x; cannot choose a compatible Cactus engine", tag)
}

// GenerationOfCactFile reports the engine generation of a .cact file.
func GenerationOfCactFile(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil {
		return 0, fmt.Errorf("%s is not a complete .cact archive: %w", path, err)
	}
	return GenerationOfCact(head)
}
