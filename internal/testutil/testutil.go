// Package testutil builds the C stub engine and the fake .cact archives
// the tests run against, mirroring the stub-based suite of the Python
// reference (tests/test_worker.py). The stub compiles with cc; tests skip
// when no C compiler is available.
package testutil

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// stubSource is a complete implementation of the cactus_* ABI with
// observable state: loads record a model id, init returns a fixed prefix,
// complete echoes envelopes for CALL:/RESPOND: prefixed inputs (so tests
// control the response envelope through the prompt), audio turns report
// the samples and the cactus_set_audio options, transcribe and the stream
// primitives echo their arguments, describe returns an FNV-1a checksum of
// the exact options bytes plus the image geometry, and embed returns
// model-relative vectors.
const stubSource = `
#include <stdint.h>
#include <stdio.h>
#include <string.h>

static int model_id = 0;
static int resets = 0;
static int threads = 0;
static int stops = 0;
static char last_lang[64] = "";
static char last_kw[512] = "";
static char last_lang_json[128] = "";
static char last_kw_json[1024] = "";
static int last_word_timestamps = 0;

static void json_escape(char* dest, size_t cap, const char* src) {
    size_t o = 0;
    for (; src && *src && o + 7 < cap; src++) {
        unsigned char c = (unsigned char)*src;
        if (c == '\\' || c == '"') { dest[o++] = '\\'; dest[o++] = (char)c; }
        else if (c == '\n') { dest[o++] = '\\'; dest[o++] = 'n'; }
        else if (c == '\r') { dest[o++] = '\\'; dest[o++] = 'r'; }
        else if (c == '\t') { dest[o++] = '\\'; dest[o++] = 't'; }
        else if (c < 0x20) { o += (size_t)snprintf(dest + o, 5, "\\u%04x", c); }
        else { dest[o++] = (char)c; }
    }
    dest[o] = 0;
}

int cactus_load(const unsigned char* data, unsigned long long size) {
    if (!data || size < 6 || data[4] == 0) return -1;
    model_id = data[4];
    return 0;
}

int cactus_models(void) { return 1 | 2 | 4; }

const char* cactus_last_error(void) { return "stub engine error"; }

int cactus_init(const char* system, const char* tools, const char* index) {
    (void)system; (void)tools; (void)index;
    return 7;
}

int cactus_complete(const char* input, const float* pcm, int samples,
                    int max_new_tokens, char* out, int capacity) {
    (void)max_new_tokens;
    if (pcm && samples >= 0) {
        snprintf(out, (size_t)capacity,
                 "{\"type\":\"text\",\"audio_text\":\"hello there\",\"audio_samples\":%d,"
                 "\"language\":\"%s\",\"keywords\":\"%s\",\"word_timestamps\":%d,"
                 "\"model\":%d,\"capacity\":%d}",
                 samples, last_lang_json, last_kw_json, last_word_timestamps, model_id, capacity);
        return 1;
    }
    if (!input) {
        snprintf(out, (size_t)capacity, "{\"type\":\"error\"}");
        return -1;
    }
    if (strncmp(input, "CALL:", 5) == 0) {
        snprintf(out, (size_t)capacity, "%s", input + 5);
        return 1;
    }
    if (strncmp(input, "RESPOND:", 8) == 0) {
        snprintf(out, (size_t)capacity, "%s", input + 8);
        return 1;
    }
    if (input[0] == '[') {
        snprintf(out, (size_t)capacity,
                 "{\"type\":\"respond\",\"model\":%d,\"resets\":%d,\"threads\":%d,\"feedback\":true}",
                 model_id, resets, threads);
        return 1;
    }
    snprintf(out, (size_t)capacity,
             "{\"type\":\"text\",\"model\":%d,\"resets\":%d,\"threads\":%d,\"input\":\"%s\"}",
             model_id, resets, threads, input);
    return 1;
}

void cactus_set_audio(const char* language, const char* keywords, int word_timestamps) {
    snprintf(last_lang, sizeof(last_lang), "%s", language ? language : "");
    snprintf(last_kw, sizeof(last_kw), "%s", keywords ? keywords : "");
    json_escape(last_lang_json, sizeof(last_lang_json), last_lang);
    json_escape(last_kw_json, sizeof(last_kw_json), last_kw);
    last_word_timestamps = word_timestamps;
}

void cactus_set_threads(int count) { threads = count; }

void cactus_reset(void) { resets++; }

int cactus_transcribe(const float* pcm, int samples, const char* language,
                      const char* keywords, int word_timestamps, char* out, int capacity) {
    (void)pcm;
    char lang_json[128], kw_json[1024];
    json_escape(lang_json, sizeof(lang_json), language);
    json_escape(kw_json, sizeof(kw_json), keywords);
    snprintf(out, (size_t)capacity,
             "{\"text\":\"samples %d\",\"language\":\"%s\",\"ttft_ms\":12.5,\"decode_tps\":900.5,"
             "\"word_timestamps\":%d,\"keywords\":\"%s\",\"capacity\":%d,"
             "\"words\":[{\"word\":\"hello\",\"start\":0.1,\"end\":0.4,\"probability\":0.98}]}",
             samples, lang_json, word_timestamps, kw_json, capacity);
    return 0;
}

int cactus_stream_transcribe_process(const float* pcm, int samples, const char* language,
                                     const char* keywords, char* out, int capacity) {
    (void)pcm;
    char lang_json[128], kw_json[1024];
    json_escape(lang_json, sizeof(lang_json), language);
    json_escape(kw_json, sizeof(kw_json), keywords);
    snprintf(out, (size_t)capacity,
             "{\"text\":\"chunk %d\",\"words\":[],\"pending\":\"tail\",\"language\":\"%s\","
             "\"received\":%.3f,\"pass_ms\":7.0,\"keywords\":\"%s\",\"capacity\":%d}",
             samples, lang_json, samples / 16000.0, kw_json, capacity);
    return 0;
}

int cactus_stream_transcribe_stop(char* out, int capacity) {
    stops++;
    snprintf(out, (size_t)capacity,
             "{\"text\":\"tail\",\"words\":[],\"pending\":\"\",\"language\":\"en\","
             "\"received\":1.0,\"pass_ms\":0.0,\"stops\":%d}", stops);
    return 0;
}

static unsigned long long fnv1a(const char* s) {
    unsigned long long h = 1469598103934665603ULL;
    for (; *s; s++) { h ^= (unsigned char)*s; h *= 1099511628211ULL; }
    return h;
}

int cactus_describe(const unsigned char* rgb, int width, int height, const char* options,
                    const char* language, const char* keywords, char* out, int capacity) {
    unsigned long long checksum = options ? fnv1a(options) : 0;
    long long sum = 0;
    int i, n = width * height * 3;
    char lang_json[128], kw_json[1024];
    if (!rgb || width <= 0 || height <= 0) return -1;
    for (i = 0; i < n && i < 1 << 24; i++) sum += rgb[i];
    json_escape(lang_json, sizeof(lang_json), language);
    json_escape(kw_json, sizeof(kw_json), keywords);
    snprintf(out, (size_t)capacity,
             "{\"options_fnv\":%llu,\"options_present\":%s,\"width\":%d,\"height\":%d,"
             "\"language\":\"%s\",\"keywords\":\"%s\",\"pixel_sum\":%lld,\"model\":%d}",
             checksum, options ? "true" : "false", width, height,
             lang_json, kw_json, sum, model_id);
    return 0;
}

int cactus_embed(const char* input, const float* pcm, int samples, float* out, int capacity) {
    int i;
    if (input) {
        if (out) { for (i = 0; i < 3 && i < capacity; i++) out[i] = (float)(model_id + i); }
        return 3;
    }
    if (pcm && samples >= 0) {
        int frames = (samples + 1279) / 1280;
        int total = frames * 512;
        if (out) { for (i = 0; i < total && i < capacity; i++) out[i] = (float)(i % 97) * 0.5f; }
        return total;
    }
    return -1;
}
`

var (
	stubOnce sync.Once
	stubPath string
	stubErr  error
)

// StubEngine compiles the stub engine once per test process and returns
// its shared-library path. The test skips when no C compiler exists.
func StubEngine(t *testing.T) string {
	t.Helper()
	stubOnce.Do(func() {
		dir, err := os.MkdirTemp("", "needle-stub-")
		if err != nil {
			stubErr = err
			return
		}
		source := filepath.Join(dir, "stub.c")
		if err := os.WriteFile(source, []byte(stubSource), 0o644); err != nil {
			stubErr = err
			return
		}
		lib := filepath.Join(dir, "libstub.so")
		cmd := exec.Command("cc", "-shared", "-fPIC", "-O1", source, "-o", lib)
		if _, err := cmd.CombinedOutput(); err != nil {
			stubErr = err
			return
		}
		stubPath = lib
	})
	if stubErr != nil {
		t.Skipf("cannot build the stub engine: %v", stubErr)
	}
	if stubPath == "" {
		t.Skip("stub engine unavailable on this platform")
	}
	return stubPath
}

// FakeCact writes a minimal generation-3 archive: the little-endian
// 0x05E12A84 tag, then the model id byte, then a filler byte. The stub
// engine reads the model id from data[4].
func FakeCact(t *testing.T, dir string, modelID byte) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "weights.cact")
	data := []byte{0x84, 0x2A, 0xE1, 0x05, modelID, 0x01}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// FakeHeadedCact writes a .cact archive carrying a head manifest, the way
// the Python test suite's _fake_cact does: an fp16 (4,) tensor, the head
// tensor with the given codes, six fp16 (2,) tensors per code, and the
// raw tokenizer record. codes nil builds a headless archive.
func FakeHeadedCact(t *testing.T, path string, codes []byte) {
	t.Helper()
	type record struct {
		dtype  uint8
		ndim   uint8
		kind   uint16
		dims   [4]uint32
		offset uint64 // into the body; shifted by the header below
		length uint64
	}
	var body []byte
	var records []record
	add := func(dtype uint8, shape []uint32, blob []byte) {
		rec := record{dtype: dtype, ndim: uint8(len(shape)), offset: uint64(len(body)), length: uint64(len(blob))}
		copy(rec.dims[:], shape)
		records = append(records, rec)
		body = append(body, blob...)
	}
	add(1, []uint32{4}, make([]byte, 8)) // four zero fp16 values
	if codes != nil {
		blob := make([]byte, 2*len(codes))
		for i, code := range codes {
			binary.LittleEndian.PutUint16(blob[i*2:], float16bits(code))
		}
		add(1, []uint32{uint32(len(codes))}, blob)
		for i := 0; i < len(codes)*6; i++ {
			add(1, []uint32{2}, make([]byte, 4))
		}
	}
	add(4, []uint32{3}, []byte("tok"))

	head := 196 + 44*len(records)
	out := make([]byte, head)
	binary.LittleEndian.PutUint32(out[0:4], 0x05E12A84)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(records)))
	binary.LittleEndian.PutUint32(out[8:12], 0) // codebook
	for i, rec := range records {
		entry := out[196+i*44 : 196+(i+1)*44]
		entry[0] = rec.dtype
		entry[1] = rec.ndim
		binary.LittleEndian.PutUint16(entry[2:4], rec.kind)
		for j, dim := range rec.dims {
			binary.LittleEndian.PutUint32(entry[4+j*4:8+j*4], dim)
		}
		binary.LittleEndian.PutUint64(entry[20:28], rec.offset+uint64(head))
		binary.LittleEndian.PutUint64(entry[28:36], rec.length)
	}
	if err := os.WriteFile(path, append(out, body...), 0o644); err != nil {
		t.Fatal(err)
	}
}

// float16bits encodes a small integer as an IEEE 754 half value: 1 ->
// 0x3C00, 2 -> 0x4000, 3 -> 0x4001, ...
func float16bits(v byte) uint16 {
	if v == 0 {
		return 0
	}
	e := 0
	m := int(v)
	for m > 1 {
		m >>= 1
		e++
	}
	mantissa := int(v) - (1 << uint(e))
	return uint16(15+e)<<10 | uint16(mantissa)
}

// FakeTokenizerCact writes a .cact archive whose raw record is a
// tokenizer table advertising the given language pieces ("<|en|>" ...),
// the way whistle.LanguagesOf reads supported languages off an archive.
func FakeTokenizerCact(t *testing.T, path string, languages []string) {
	t.Helper()
	// The tokenizer table: a piece count, 20 bytes of header, then
	// 7-byte entries (5 header bytes, LE16 length, piece bytes).
	blob := make([]byte, 24)
	binary.LittleEndian.PutUint32(blob[0:4], uint32(len(languages)))
	for _, lang := range languages {
		piece := "<|" + lang + "|>"
		var header [7]byte
		binary.LittleEndian.PutUint16(header[5:7], uint16(len(piece)))
		blob = append(blob, header[:]...)
		blob = append(blob, piece...)
	}
	head := 196 + 44*2
	out := make([]byte, head, head+8+len(blob))
	binary.LittleEndian.PutUint32(out[0:4], 0x05E12A84)
	binary.LittleEndian.PutUint32(out[4:8], 2)
	binary.LittleEndian.PutUint32(out[8:12], 0)
	// Record 0: fp16 (4,) placeholder weights tensor.
	rec0 := out[196 : 196+44]
	rec0[0] = 1 // fp16
	rec0[1] = 1
	binary.LittleEndian.PutUint32(rec0[4:8], 4)
	binary.LittleEndian.PutUint64(rec0[20:28], uint64(head))
	binary.LittleEndian.PutUint64(rec0[28:36], 8)
	// Record 1: the raw tokenizer table.
	rec1 := out[196+44 : 196+88]
	rec1[0] = 4 // raw
	rec1[1] = 1
	binary.LittleEndian.PutUint32(rec1[4:8], uint32(len(blob)))
	binary.LittleEndian.PutUint64(rec1[20:28], uint64(head+8))
	binary.LittleEndian.PutUint64(rec1[28:36], uint64(len(blob)))
	out = append(out, make([]byte, 8)...)
	out = append(out, blob...)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}
