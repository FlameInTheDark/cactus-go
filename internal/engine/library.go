package engine

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// native holds the C symbols of one loaded engine library, as declared in
// cactus.h. All int-returning functions yield a negative code on failure.
type native struct {
	load          func(data *byte, size uint64) int32
	models        func() int32
	lastError     func() unsafe.Pointer
	init          func(system, tools, index *byte) int32
	complete      func(input *byte, pcm *float32, samples int32, maxTokens int32, out *byte, outCap int32) int32
	setAudio      func(language, keywords *byte, wordTimestamps int32)
	setThreads    func(threads int32)
	reset         func()
	transcribe    func(pcm *float32, samples int32, language, keywords *byte, wordTimestamps int32, out *byte, outCap int32) int32
	streamProcess func(pcm *float32, samples int32, language, keywords *byte, out *byte, outCap int32) int32
	streamStop    func(out *byte, outCap int32) int32
	describe      func(rgb *byte, width, height int32, options, language, keywords *byte, out *byte, outCap int32) int32
	embed         func(input *byte, pcm *float32, samples int32, out *float32, outCap int32) int32
}

// Library is a loaded Cactus engine with its symbols bound. Libraries are
// opened once per path and reused for every later call.
type Library struct {
	path string
	lib  *native
}

var (
	libsMu   sync.Mutex
	libs     = map[string]*Library{} // keyed by resolved library path
	loadErrs = map[string]error{}
)

// LoadNative opens the shared library at path and binds its symbols. The
// result is cached, so repeated calls with the same path are cheap.
// Loading goes through purego's dlopen on Unix and the stdlib
// syscall.LoadLibrary on Windows - no cgo anywhere.
func LoadNative(path string) (*Library, error) {
	libsMu.Lock()
	defer libsMu.Unlock()
	if lib, ok := libs[path]; ok {
		return lib, nil
	}
	if err, ok := loadErrs[path]; ok {
		return nil, err
	}
	handle, err := openLibrary(path)
	if err != nil {
		err = fmt.Errorf("cactus: cannot open engine library %s: %w", path, err)
		loadErrs[path] = err
		return nil, err
	}
	n := &native{}
	if err := bindNative(n, handle); err != nil {
		closeLibrary(handle)
		loadErrs[path] = err
		return nil, err
	}
	lib := &Library{path: path, lib: n}
	libs[path] = lib
	return lib, nil
}

// Path reports the path the library was loaded from.
func (l *Library) Path() string { return l.path }

func bindNative(n *native, handle uintptr) error {
	bind := func(name string, assign func()) error {
		if _, err := lookupSymbol(handle, name); err != nil {
			return fmt.Errorf("cactus: engine library is missing symbol %s: %w", name, err)
		}
		assign()
		return nil
	}
	steps := []struct {
		name   string
		assign func()
	}{
		{"cactus_load", func() { purego.RegisterLibFunc(&n.load, handle, "cactus_load") }},
		{"cactus_models", func() { purego.RegisterLibFunc(&n.models, handle, "cactus_models") }},
		{"cactus_last_error", func() { purego.RegisterLibFunc(&n.lastError, handle, "cactus_last_error") }},
		{"cactus_init", func() { purego.RegisterLibFunc(&n.init, handle, "cactus_init") }},
		{"cactus_complete", func() { purego.RegisterLibFunc(&n.complete, handle, "cactus_complete") }},
		{"cactus_set_audio", func() { purego.RegisterLibFunc(&n.setAudio, handle, "cactus_set_audio") }},
		{"cactus_set_threads", func() { purego.RegisterLibFunc(&n.setThreads, handle, "cactus_set_threads") }},
		{"cactus_reset", func() { purego.RegisterLibFunc(&n.reset, handle, "cactus_reset") }},
		{"cactus_transcribe", func() { purego.RegisterLibFunc(&n.transcribe, handle, "cactus_transcribe") }},
		{"cactus_stream_transcribe_process", func() { purego.RegisterLibFunc(&n.streamProcess, handle, "cactus_stream_transcribe_process") }},
		{"cactus_stream_transcribe_stop", func() { purego.RegisterLibFunc(&n.streamStop, handle, "cactus_stream_transcribe_stop") }},
		{"cactus_describe", func() { purego.RegisterLibFunc(&n.describe, handle, "cactus_describe") }},
		{"cactus_embed", func() { purego.RegisterLibFunc(&n.embed, handle, "cactus_embed") }},
	}
	for _, step := range steps {
		if err := bind(step.name, step.assign); err != nil {
			return err
		}
	}
	return nil
}

// cstring converts a Go string to a NUL-terminated C string, returning nil
// for the empty string so the engine sees a NULL pointer.
func cstring(s string) *byte {
	if s == "" {
		return nil
	}
	b := append(make([]byte, 0, len(s)+1), s...)
	b = append(b, 0)
	return &b[0]
}

// cStringAt reads a NUL-terminated C string from a pointer the engine
// returned.
func cStringAt(p unsafe.Pointer) string {
	if p == nil {
		return ""
	}
	const max = 1 << 16
	raw := (*[max]byte)(p)
	n := 0
	for n < max && raw[n] != 0 {
		n++
	}
	return string(raw[:n:n])
}

// LastError returns the engine's last error detail, or "" when none.
func (l *Library) LastError() string {
	return cStringAt(l.lib.lastError())
}

// Models returns the bitmask of model types loaded in the engine
// (ModelText | ModelSpeech | ModelVision).
func (l *Library) Models() int32 {
	return l.lib.models()
}

// The shared runtime state. The engine keeps global state, so every call
// is serialized by rtMu and model archives are loaded at most once per
// path per process.
var (
	rtMu        sync.Mutex
	threads     int32               // cactus_set_threads value, 0 = automatic
	loadedPaths = map[string]bool{} // absolute .cact paths already loaded
	activeText  *TextSession        // the session currently bound via cactus_init
)

// SetThreads sets the CPU thread count for every model loaded after the
// call, worker children included, or 0 (the default) to use the cores the
// process may run on. Negative values are rejected.
func SetThreads(n int) error {
	if n < 0 {
		return fmt.Errorf("cactus: threads must be a whole number, or 0 for the automatic count")
	}
	rtMu.Lock()
	defer rtMu.Unlock()
	threads = int32(n)
	for _, lib := range libs {
		lib.lib.setThreads(int32(n))
	}
	return nil
}

// Threads reports the configured thread count.
func Threads() int {
	rtMu.Lock()
	defer rtMu.Unlock()
	return int(threads)
}

// EnsureWeights loads a .cact archive into the shared engine unless the
// archive at that path is already loaded. Loading a new archive
// invalidates the active text session, exactly as the Python runtime
// does.
func (l *Library) EnsureWeights(path string) error {
	rtMu.Lock()
	defer rtMu.Unlock()
	return l.ensureWeightsLocked(path)
}

func (l *Library) ensureWeightsLocked(path string) error {
	if loadedPaths[path] {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("cactus: weights archive %s is empty", path)
	}
	if rc := l.lib.load(&data[0], uint64(len(data))); rc < 0 {
		detail := l.LastError()
		if detail == "" {
			detail = fmt.Sprintf("cactus_load failed for %s (code %d)", path, rc)
		}
		return fmt.Errorf("%s", detail)
	}
	loadedPaths[path] = true
	activeText = nil
	return nil
}

// WeightsLoaded reports whether the archive at path was already loaded
// into this process.
func WeightsLoaded(path string) bool {
	rtMu.Lock()
	defer rtMu.Unlock()
	return loadedPaths[path]
}

// CactHeaderSize is the fixed size of the .cact archive header.
const CactHeaderSize = 196

// CactRecord is one entry of a .cact tensor directory. Offset and Length
// address the tensor bytes inside the archive.
type CactRecord struct {
	Dtype  uint8
	NDim   uint8
	Kind   uint16
	Dims   [4]uint32
	Offset uint64
	Length uint64
}

// .cact dtype codes.
const (
	DtypeFP16 = 1
	DtypeRaw  = 4
)

// ReadCactRecords parses the header and tensor directory of a .cact
// archive without loading its payload. It returns the format tag, the
// record list and the byte offset where the tensor data region starts.
func ReadCactRecords(data []byte) (tag uint32, records []CactRecord, dataStart int, err error) {
	if len(data) < CactHeaderSize {
		return 0, nil, 0, fmt.Errorf("not a complete .cact archive")
	}
	tag = binary.LittleEndian.Uint32(data[0:4])
	count := binary.LittleEndian.Uint32(data[4:8])
	codebook := binary.LittleEndian.Uint32(data[8:12])
	start := CactHeaderSize + int(codebook)*4
	end := start + int(count)*44
	if count > 1<<20 || end > len(data) {
		return tag, nil, 0, fmt.Errorf("not a complete .cact archive: directory of %d tensors overruns the file", count)
	}
	records = make([]CactRecord, 0, count)
	for i := 0; i < int(count); i++ {
		rec := data[start+i*44 : start+(i+1)*44]
		records = append(records, CactRecord{
			Dtype: rec[0],
			NDim:  rec[1],
			Kind:  binary.LittleEndian.Uint16(rec[2:4]),
			Dims: [4]uint32{
				binary.LittleEndian.Uint32(rec[4:8]),
				binary.LittleEndian.Uint32(rec[8:12]),
				binary.LittleEndian.Uint32(rec[12:16]),
				binary.LittleEndian.Uint32(rec[16:20]),
			},
			Offset: binary.LittleEndian.Uint64(rec[20:28]),
			Length: binary.LittleEndian.Uint64(rec[28:36]),
		})
	}
	return tag, records, end, nil
}

// Transcribe runs one-pass speech-to-text over the samples, writing the
// JSON result into out and returning it. A negative engine code becomes
// an error carrying cactus_last_error's detail.
func (l *Library) Transcribe(samples []float32, language, keywords string, wordTimestamps bool, out []byte) (string, error) {
	rtMu.Lock()
	defer rtMu.Unlock()
	return l.transcribeLocked(samples, language, keywords, wordTimestamps, out)
}

func (l *Library) transcribeLocked(samples []float32, language, keywords string, wordTimestamps bool, out []byte) (string, error) {
	pcm, count := float32Ptr(samples)
	timestamps := int32(0)
	if wordTimestamps {
		timestamps = 1
	}
	rc := l.lib.transcribe(pcm, int32(count), cstring(language), cstring(keywords), timestamps, &out[0], int32(len(out)))
	if rc < 0 {
		return "", l.engineError("cactus_transcribe", rc)
	}
	return cbufferString(out), nil
}

// StreamProcess feeds one chunk to the live transcriber, returning the
// JSON step the engine committed for it. It must be called while holding
// Lock, so a whole stream stays atomic against other engine calls.
func (l *Library) StreamProcess(samples []float32, language, keywords string, out []byte) (string, error) {
	pcm, count := float32Ptr(samples)
	rc := l.lib.streamProcess(pcm, int32(count), cstring(language), cstring(keywords), &out[0], int32(len(out)))
	if rc < 0 {
		return "", l.engineError("cactus_stream_transcribe_process", rc)
	}
	return cbufferString(out), nil
}

// StreamStop finishes a live transcription and returns the tail step. It
// must be called while holding Lock.
func (l *Library) StreamStop(out []byte) (string, error) {
	rc := l.lib.streamStop(&out[0], int32(len(out)))
	if rc < 0 {
		return "", l.engineError("cactus_stream_transcribe_stop", rc)
	}
	return cbufferString(out), nil
}

// Describe hands a packed RGB image with its options to the vision model.
func (l *Library) Describe(rgb []byte, width, height int, options, language, keywords string, out []byte) (string, error) {
	rtMu.Lock()
	defer rtMu.Unlock()
	if len(rgb) == 0 {
		return "", fmt.Errorf("cactus: describe needs image bytes")
	}
	rc := l.lib.describe(&rgb[0], int32(width), int32(height), cstring(options), cstring(language), cstring(keywords), &out[0], int32(len(out)))
	if rc < 0 {
		return "", l.engineError("cactus_describe", rc)
	}
	return cbufferString(out), nil
}

// EmbedText returns the text embedding of the input.
func (l *Library) EmbedText(text string) ([]float32, error) {
	rtMu.Lock()
	defer rtMu.Unlock()
	return l.embedTextLocked(text)
}

func (l *Library) embedTextLocked(text string) ([]float32, error) {
	input := cstring(text)
	dim := l.lib.embed(input, nil, 0, nil, 0)
	if dim <= 0 {
		return nil, l.engineError("cactus_embed", dim)
	}
	out := make([]float32, dim)
	if rc := l.lib.embed(input, nil, 0, &out[0], dim); rc != dim {
		return nil, l.engineError("cactus_embed", rc)
	}
	return out, nil
}

// EmbedAudio returns the speech encoder output for the samples: one row
// of floats per 80 ms frame, flattened.
func (l *Library) EmbedAudio(samples []float32) ([]float32, error) {
	rtMu.Lock()
	defer rtMu.Unlock()
	if len(samples) == 0 {
		return nil, fmt.Errorf("cactus: embed needs audio samples")
	}
	size := l.lib.embed(nil, &samples[0], int32(len(samples)), nil, 0)
	if size < 0 {
		return nil, l.engineError("cactus_embed", size)
	}
	out := make([]float32, size)
	if rc := l.lib.embed(nil, &samples[0], int32(len(samples)), &out[0], size); rc != size {
		return nil, l.engineError("cactus_embed", rc)
	}
	return out, nil
}

// engineError builds the error for a negative engine code, preferring the
// engine's own detail string.
func (l *Library) engineError(op string, rc int32) error {
	if detail := l.LastError(); detail != "" {
		return fmt.Errorf("%s", detail)
	}
	return fmt.Errorf("cactus: %s failed (code %d)", op, rc)
}

// WithLock runs fn while holding the shared engine lock, so callers can
// keep a multi-step engine interaction (for example a live transcription
// stream) from interleaving with other engine calls. Functions that lock
// internally (Transcribe, Describe, EmbedText, EmbedAudio) must not be
// called inside; use the stream primitives instead.
func WithLock(fn func()) {
	rtMu.Lock()
	defer rtMu.Unlock()
	fn()
}
