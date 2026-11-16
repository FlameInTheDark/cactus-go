package engine

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// TextSession is a Backend that calls the native engine in the current
// process. It is the backend used for the base model, whose weights are
// loaded once per process; switching sessions reconfigures the engine's
// single text context (which also rewinds the conversation).
type TextSession struct {
	lib        *Library
	system     string
	toolsJSON  string
	toolIndex  string
	baseWeight string // base archive this session requires before init
	buf        []byte
	prefixTok  int
	closed     bool
}

// NewTextSession creates an in-process text backend for the library at
// libPath. baseWeights is the archive loaded into the engine before the
// session binds (the Needle 3 base model); it is loaded at most once per
// process.
func NewTextSession(libPath, system, toolsJSON, toolIndex, baseWeights string, bufSize int) (*TextSession, error) {
	lib, err := LoadNative(libPath)
	if err != nil {
		return nil, err
	}
	if bufSize <= 0 {
		bufSize = 65536
	}
	return &TextSession{
		lib:        lib,
		system:     system,
		toolsJSON:  toolsJSON,
		toolIndex:  toolIndex,
		baseWeight: baseWeights,
		buf:        make([]byte, bufSize),
	}, nil
}

// PrefixTokens reports the prompt prefix token count reported by the last
// successful cactus_init call for this session.
func (s *TextSession) PrefixTokens() int {
	rtMu.Lock()
	defer rtMu.Unlock()
	return s.prefixTok
}

// bind reconfigures the engine for this session if it is not the active
// one; rtMu must be held.
func (s *TextSession) bind() error {
	if s.closed {
		return fmt.Errorf("cactus: session is closed")
	}
	if s.baseWeight != "" {
		if err := s.lib.ensureWeightsLocked(s.baseWeight); err != nil {
			return err
		}
	}
	if activeText == s {
		return nil
	}
	rc := s.lib.lib.init(cstring(s.system), cstring(s.toolsJSON), cstring(s.toolIndex))
	if rc < 0 {
		detail := s.lib.LastError()
		activeText = nil
		if detail != "" {
			return fmt.Errorf("%s", detail)
		}
		return fmt.Errorf("cactus: cactus_init failed (code %d)", rc)
	}
	activeText = s
	s.prefixTok = int(rc)
	return nil
}

// Complete runs one text inference turn in-process.
func (s *TextSession) Complete(_ context.Context, input string, maxNewTokens int) (string, error) {
	rtMu.Lock()
	defer rtMu.Unlock()
	if err := s.bind(); err != nil {
		return "", err
	}
	if maxNewTokens <= 0 {
		maxNewTokens = 512
	}
	rc := s.lib.lib.complete(cstring(input), nil, 0, int32(maxNewTokens), &s.buf[0], int32(len(s.buf)))
	if rc < 0 {
		return "", s.failComplete("cactus_complete", rc, s.buf)
	}
	return cbufferString(s.buf), nil
}

// CompleteAudio runs one speech-driven turn: the engine transcribes with
// the options set by cactus_set_audio and feeds the transcript through the
// tool-calling grammar in one pass. The speech weights are loaded on
// demand.
func (s *TextSession) CompleteAudio(_ context.Context, samples []float32, opts AudioOptions, maxNewTokens int) (string, error) {
	rtMu.Lock()
	defer rtMu.Unlock()
	if err := s.bind(); err != nil {
		return "", err
	}
	if opts.SpeechWeights != "" {
		if err := s.lib.ensureWeightsLocked(opts.SpeechWeights); err != nil {
			return "", err
		}
	}
	if maxNewTokens <= 0 {
		maxNewTokens = 512
	}
	pcm, count := float32Ptr(samples)
	timestamps := int32(0)
	if opts.WordTimestamps {
		timestamps = 1
	}
	s.lib.lib.setAudio(cstring(opts.Language), cstring(opts.Keywords), timestamps)
	rc := s.lib.lib.complete(nil, pcm, int32(count), int32(maxNewTokens), &s.buf[0], int32(len(s.buf)))
	if rc < 0 {
		detail := s.lib.LastError()
		if detail == "" {
			detail = fmt.Sprintf("cactus: cactus_complete failed (code %d)", rc)
		}
		return "", fmt.Errorf("%s", detail)
	}
	return cbufferString(s.buf), nil
}

// Embed returns the text embedding of the input. The dimension is queried
// first (null output buffer), then the vector is filled.
func (s *TextSession) Embed(_ context.Context, text string) ([]float32, error) {
	rtMu.Lock()
	defer rtMu.Unlock()
	if err := s.bind(); err != nil {
		return nil, err
	}
	input := cstring(text)
	dim := s.lib.lib.embed(input, nil, 0, nil, 0)
	if dim <= 0 {
		detail := s.lib.LastError()
		if detail == "" {
			detail = fmt.Sprintf("cactus: cactus_embed failed (code %d)", dim)
		}
		return nil, fmt.Errorf("%s", detail)
	}
	out := make([]float32, dim)
	if rc := s.lib.lib.embed(input, nil, 0, &out[0], dim); rc != dim {
		detail := s.lib.LastError()
		if detail == "" {
			detail = fmt.Sprintf("cactus: cactus_embed failed (code %d)", rc)
		}
		return nil, fmt.Errorf("%s", detail)
	}
	return out, nil
}

// Reset rewinds the conversation of the active session.
func (s *TextSession) Reset() error {
	rtMu.Lock()
	defer rtMu.Unlock()
	if s.closed {
		return fmt.Errorf("cactus: session is closed")
	}
	if err := s.bind(); err != nil {
		return err
	}
	s.lib.lib.reset()
	return nil
}

// Close marks the session closed. The shared library itself stays loaded
// for the lifetime of the process.
func (s *TextSession) Close() error {
	rtMu.Lock()
	defer rtMu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if activeText == s {
		activeText = nil
	}
	return nil
}

// failComplete builds the error for a negative cactus_complete code: the
// engine writes the detail into the output buffer, with last_error and
// the code as fallbacks.
func (s *TextSession) failComplete(op string, rc int32, buf []byte) error {
	if detail := cbufferString(buf); detail != "" {
		return fmt.Errorf("%s", detail)
	}
	if detail := s.lib.LastError(); detail != "" {
		return fmt.Errorf("%s", detail)
	}
	return fmt.Errorf("cactus: %s failed (code %d)", op, rc)
}

// float32Ptr returns a pointer to the first sample and the sample count,
// tolerating empty slices.
func float32Ptr(samples []float32) (*float32, int) {
	if len(samples) == 0 {
		return nil, 0
	}
	return &samples[0], len(samples)
}

// cbufferString reads the NUL-terminated message the engine wrote into the
// response buffer, stopping at the first NUL and sanitizing invalid UTF-8.
func cbufferString(buf []byte) string {
	n := bytes.IndexByte(buf, 0)
	if n < 0 {
		n = len(buf)
	}
	s := string(buf[:n])
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	return s
}
