package audio

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// tone synthesizes a 220 Hz sine at the given rate.
func tone(seconds float64, rate int) []float64 {
	n := int(seconds * float64(rate))
	out := make([]float64, n)
	for i := range out {
		out[i] = 0.3 * math.Sin(2*math.Pi*220*float64(i)/float64(rate))
	}
	return out
}

// writeWav writes an interleaved WAV with the given sample width.
func writeWav(t *testing.T, path string, samples []float64, rate, channels, width int, floatFormat bool) {
	t.Helper()
	data := make([]byte, 0, len(samples)*channels*width)
	for _, value := range samples {
		for c := 0; c < channels; c++ {
			switch {
			case floatFormat && width == 4:
				var buf [4]byte
				binary.LittleEndian.PutUint32(buf[:], math.Float32bits(float32(value)))
				data = append(data, buf[:]...)
			case floatFormat && width == 8:
				var buf [8]byte
				binary.LittleEndian.PutUint64(buf[:], math.Float64bits(value))
				data = append(data, buf[:]...)
			case width == 1:
				data = append(data, byte(int(value*127)+128))
			case width == 2:
				var buf [2]byte
				binary.LittleEndian.PutUint16(buf[:], uint16(int16(value*32767)))
				data = append(data, buf[:]...)
			case width == 3:
				v := int32(value * 8388607)
				data = append(data, byte(v), byte(v>>8), byte(v>>16))
			case width == 4:
				var buf [4]byte
				binary.LittleEndian.PutUint32(buf[:], uint32(int32(value*2147483647)))
				data = append(data, buf[:]...)
			}
		}
	}
	tag := uint16(1)
	if floatFormat {
		tag = 3
	}
	writeRiff(t, path, riffChunks(tag, channels, rate, width, data, false, nil, nil))
}

func riffChunks(tag uint16, channels, rate, width int, payload []byte, extensible bool, before, after []byte) []byte {
	format := make([]byte, 16)
	binary.LittleEndian.PutUint16(format[0:2], tag)
	binary.LittleEndian.PutUint16(format[2:4], uint16(channels))
	binary.LittleEndian.PutUint32(format[4:8], uint32(rate))
	binary.LittleEndian.PutUint32(format[8:12], uint32(rate*channels*width))
	binary.LittleEndian.PutUint16(format[12:14], uint16(channels*width))
	binary.LittleEndian.PutUint16(format[14:16], uint16(8*width))
	if extensible {
		// WAVEFORMATEXTENSIBLE: re-tag as 0xFFFE, keep the base fields and
		// append cbSize=22, valid bits, channel mask and the sub-format
		// GUID whose first two bytes are the real tag.
		base := append([]byte(nil), format...)
		base[0], base[1] = 0xFE, 0xFF
		format = base
		format = append(format, 22, 0) // cbSize
		format = append(format, uint8(8*width), 0)
		format = append(format, 0x01, 0x00, 0x00, 0x00) // channel mask
		guid := []byte{0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}
		realTag := []byte{byte(tag), byte(tag >> 8)}
		format = append(format, realTag...)
		format = append(format, guid[2:]...)
	}
	chunk := func(name string, body []byte) []byte {
		out := []byte(name)
		var size [4]byte
		binary.LittleEndian.PutUint32(size[:], uint32(len(body)))
		out = append(out, size[:]...)
		out = append(out, body...)
		if len(body)%2 == 1 {
			out = append(out, 0)
		}
		return out
	}
	body := []byte("WAVE")
	body = append(body, before...)
	body = append(body, chunk("fmt ", format)...)
	body = append(body, after...)
	body = append(body, chunk("data", payload)...)
	out := []byte("RIFF")
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], uint32(len(body)))
	out = append(out, size[:]...)
	out = append(out, body...)
	return out
}

func writeRiff(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWavFilesBecome16kMonoFloats ports the Python matrix: every common
// sample layout must produce the same 16 kHz mono floats.
func TestWavFilesBecome16kMonoFloats(t *testing.T) {
	cases := []struct {
		name     string
		rate     int
		channels int
		width    int
		floatFmt bool
	}{
		{"pcm16 mono", 16000, 1, 2, false},
		{"pcm16 8k", 8000, 1, 2, false},
		{"pcm16 44.1k stereo", 44100, 2, 2, false},
		{"pcm8 mono", 16000, 1, 1, false},
		{"pcm24 22.05k stereo", 22050, 2, 3, false},
		{"pcm32 48k", 48000, 1, 4, false},
		{"float32 mono", 16000, 1, 4, true},
		{"float64 stereo", 16000, 2, 8, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tone.wav")
			writeWav(t, path, tone(0.5, tc.rate), tc.rate, tc.channels, tc.width, tc.floatFmt)
			samples, err := ReadWAV(path)
			if err != nil {
				t.Fatal(err)
			}
			if abs(len(samples)-8000) > 1 {
				t.Fatalf("len = %d, want ~8000 (0.5 s at 16 kHz)", len(samples))
			}
			max, min := 0.0, 0.0
			for _, s := range samples {
				max = math.Max(max, float64(s))
				min = math.Min(min, float64(s))
			}
			if !(0.28 < max && max < 0.31) || !(-0.31 < min && min < -0.28) {
				t.Fatalf("amplitude out of range: max=%.3f min=%.3f", max, min)
			}
			crossings := 0
			for i := 1; i < len(samples); i++ {
				if samples[i-1] < 0 && samples[i] >= 0 {
					crossings++
				}
			}
			// 220 Hz over 0.5 s is 110 periods; resampling may drop boundary ones.
			if crossings < 108 || crossings > 111 {
				t.Fatalf("zero crossings = %d, want 108..111", crossings)
			}
		})
	}
}

func TestFloatAndExtensibleWavFilesReadLikePCM(t *testing.T) {
	values := []float64{0.0, 0.5, -0.25, 0.75}
	dir := t.TempDir()
	packF32 := func(vals ...float64) []byte {
		out := make([]byte, 4*len(vals))
		for i, v := range vals {
			binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(float32(v)))
		}
		return out
	}
	packF64 := func(vals ...float64) []byte {
		out := make([]byte, 8*len(vals))
		for i, v := range vals {
			binary.LittleEndian.PutUint64(out[i*8:], math.Float64bits(v))
		}
		return out
	}
	packI16 := func(vals ...float64) []byte {
		out := make([]byte, 2*len(vals))
		for i, v := range vals {
			binary.LittleEndian.PutUint16(out[i*2:], uint16(int16(v*32768)))
		}
		return out
	}
	stereo := func(vals []float64) []float64 {
		var out []float64
		for _, v := range vals {
			out = append(out, v, v)
		}
		return out
	}
	cases := map[string][]byte{
		"float32":         riffChunks(3, 1, 16000, 4, packF32(values...), false, nil, nil),
		"float64 stereo":  riffChunks(3, 2, 16000, 8, packF64(stereo(values)...), false, nil, nil),
		"extensible i16":  riffChunks(1, 1, 16000, 2, packI16(values...), true, nil, nil),
		"extensible f32":  riffChunks(3, 1, 16000, 4, packF32(values...), true, nil, nil),
		"odd chunk first": riffChunks(3, 1, 16000, 4, packF32(values...), false, []byte("LIST\x05\x00\x00\x00abcde\x00"), nil),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".wav")
			writeRiff(t, path, data)
			samples, err := ReadWAV(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(samples) != len(values) {
				t.Fatalf("len = %d, want %d", len(samples), len(values))
			}
			for i := range values {
				if math.Abs(float64(samples[i])-values[i]) > 1e-4 {
					t.Fatalf("sample %d = %.4f, want %.4f", i, samples[i], values[i])
				}
			}
		})
	}
}

func TestFilesThatAreNotPcmOrFloatWavFail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.wav")
	for name, data := range map[string][]byte{
		"empty":    {},
		"flac":     append([]byte("fLaC"), make([]byte, 40)...),
		"riffless": []byte("RIFF\x04\x00\x00\x00WAVE"),
		"bad tag":  riffChunks(7, 1, 8000, 1, make([]byte, 8), false, nil, nil),
	} {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadWAV(path); err == nil {
			t.Fatalf("%s must fail to parse", name)
		} else if _, ok := err.(*Error); !ok {
			t.Fatalf("%s must fail with *audio.Error, got %T", name, err)
		}
	}
	// An empty but valid WAV yields zero samples, not an error.
	writeRiff(t, path, riffChunks(1, 1, 16000, 2, nil, false, nil, nil))
	samples, err := ReadWAV(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 0 {
		t.Fatalf("empty WAV yielded %d samples", len(samples))
	}
}

func TestSamplesAcceptsBytesAndSlices(t *testing.T) {
	values := []float32{0.0, 0.5, -0.25}
	raw := make([]byte, 4*len(values))
	for i, v := range values {
		binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(v))
	}
	doubles := make([]float64, len(values))
	for i, v := range values {
		doubles[i] = float64(v)
	}
	for name, input := range map[string]any{
		"float32 slice": values,
		"bytes":         raw,
		"float64 slice": doubles,
		"nil":           nil,
	} {
		t.Run(name, func(t *testing.T) {
			samples, err := Samples(input)
			if err != nil {
				t.Fatal(err)
			}
			if name == "nil" {
				if len(samples) != 0 {
					t.Fatal("nil input must yield no samples")
				}
				return
			}
			if len(samples) != len(values) {
				t.Fatalf("len = %d", len(samples))
			}
			for i := range values {
				if samples[i] != values[i] {
					t.Fatalf("sample %d = %v, want %v", i, samples[i], values[i])
				}
			}
		})
	}
	if _, err := Samples(42); err == nil {
		t.Fatal("unsupported input types must be rejected")
	}
}

func TestOptionsNormalizesKeywords(t *testing.T) {
	language, keywords, err := Options("pl", []string{"Łódź", "café"})
	if err != nil {
		t.Fatal(err)
	}
	if language != "pl" || keywords != "Łódź\ncafé" {
		t.Fatalf("got %q / %q", language, keywords)
	}
	if _, keywords, err := Options("", "one\ntwo"); err != nil || keywords != "one\ntwo" {
		t.Fatalf("string keywords must pass verbatim: %q %v", keywords, err)
	}
	if _, keywords, err := Options("", nil); err != nil || keywords != "" {
		t.Fatalf("nil keywords must stay empty: %q %v", keywords, err)
	}
	if _, _, err := Options("", 42); err == nil {
		t.Fatal("unsupported keyword types must be rejected")
	}
}

func TestResamplePreservesIdentityWhenRatesMatch(t *testing.T) {
	samples := []float32{0.1, -0.2, 0.3}
	if out := Resample(samples, 16000, 16000); &out[0] != &samples[0] {
		t.Fatal("matching rates must return the input unchanged")
	}
}

func TestResampleUpsamplesATone(t *testing.T) {
	// A 220 Hz tone at 8 kHz upsampled to 16 kHz must keep its period.
	source := tone(0.25, 8000)
	samples := make([]float32, len(source))
	for i, v := range source {
		samples[i] = float32(v)
	}
	out := Resample(samples, 8000, 16000)
	if abs(len(out)-len(samples)*2) > 2 {
		t.Fatalf("len = %d, want ~%d", len(out), len(samples)*2)
	}
	crossings := 0
	for i := 1; i < len(out); i++ {
		if out[i-1] < 0 && out[i] >= 0 {
			crossings++
		}
	}
	// 0.25 s of 220 Hz is 55 periods.
	if crossings < 53 || crossings > 57 {
		t.Fatalf("zero crossings = %d, want ~55", crossings)
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
