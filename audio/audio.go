// Package audio turns the audio inputs the Cactus APIs accept into the one
// form the engine consumes: 16 kHz mono float32 samples in [-1, 1].
//
// Accepted inputs are a WAV file path, raw little-endian float32 sample
// bytes, or a slice of float32/float64 samples. WAV files may be PCM
// (u8/s16/s24/s32) or IEEE float (f32/f64), mono or multi-channel (frames
// are averaged), optionally in the WAVE_FORMAT_EXTENSIBLE layout. Any
// other sample rate is resampled to 16 kHz with a pure-Go Kaiser-windowed
// sinc filter; the Python reference uses soxr for this, which this library
// deliberately replaces to stay dependency-free.
package audio

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"strings"
	"unsafe"
)

// SampleRate is the engine's audio input rate in hertz.
const SampleRate = 16000

// Error is returned for inputs that are not PCM or float WAV files.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func errWAV(format string, args ...any) error {
	return &Error{Msg: fmt.Sprintf(format, args...)}
}

func float32frombits(v uint32) float32 { return *(*float32)(unsafe.Pointer(&v)) }

// Samples converts an accepted audio input into engine samples.
//
//   - string: a WAV file path (any rate, any common sample format)
//   - []byte: raw little-endian float32 samples
//   - []float32 / []float64: samples as-is
//
// WAV inputs at a rate other than 16 kHz are resampled.
func Samples(input any) ([]float32, error) {
	switch v := input.(type) {
	case nil:
		return nil, nil
	case string:
		return ReadWAV(v)
	case []byte:
		return fromFloat32Bytes(v), nil
	case []float32:
		return v, nil
	case []float64:
		out := make([]float32, len(v))
		for i, s := range v {
			out[i] = float32(s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("cactus: unsupported audio input of type %T; pass a WAV path, []byte float32 LE samples, []float32 or []float64", input)
}

// fromFloat32Bytes decodes little-endian float32 PCM bytes.
func fromFloat32Bytes(raw []byte) []float32 {
	n := len(raw) / 4
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return out
}

// ReadWAV reads a WAV file into 16 kHz mono float32 samples in [-1, 1],
// resampling when the file's rate differs from SampleRate.
func ReadWAV(path string) ([]float32, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rate, mono, err := decodeWAV(raw)
	if err != nil {
		return nil, errWAV("%s: %s", path, err.Error())
	}
	if rate == SampleRate || len(mono) == 0 {
		return mono, nil
	}
	return Resample(mono, rate, SampleRate), nil
}

// decodeWAV parses RIFF chunks and returns the frame rate with mono
// float32 samples averaged across channels.
func decodeWAV(data []byte) (int, []float32, error) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0, nil, errWAV("not a WAV file")
	}
	chunks := map[string][]byte{}
	at := 12
	for at+8 <= len(data) {
		name := string(data[at : at+4])
		size := int(binary.LittleEndian.Uint32(data[at+4 : at+8]))
		if size < 0 || at+8+size > len(data) {
			break
		}
		if _, seen := chunks[name]; !seen {
			chunks[name] = data[at+8 : at+8+size]
		}
		at += 8 + size + (size & 1)
	}
	format, ok := chunks["fmt "]
	if !ok || len(format) < 16 {
		return 0, nil, errWAV("not a WAV file: missing fmt chunk")
	}
	payload, ok := chunks["data"]
	if !ok {
		return 0, nil, errWAV("not a WAV file: missing data chunk")
	}
	tag := binary.LittleEndian.Uint16(format[0:2])
	channels := int(binary.LittleEndian.Uint16(format[2:4]))
	rate := int(binary.LittleEndian.Uint32(format[4:8]))
	align := int(binary.LittleEndian.Uint16(format[12:14]))
	if tag == 0xFFFE && len(format) >= 26 { // WAVE_FORMAT_EXTENSIBLE
		tag = binary.LittleEndian.Uint16(format[24:26])
	}
	if channels <= 0 {
		return 0, nil, errWAV("not a WAV file: no channels")
	}
	if align <= 0 || align%channels != 0 {
		align = channels * 2
	}
	width := align / channels
	offset, scale := 0.0, 1.0
	switch {
	case tag == 3: // IEEE float: values are already in [-1, 1]
	case tag == 1 && width == 1:
		offset, scale = 128, 128.0
	case tag == 1:
		scale = float64(int64(1) << (8*width - 1))
	default:
		return 0, nil, errWAV("not integer PCM or float WAV (tag %d, width %d)", tag, width)
	}
	mono, err := framesToMono(payload, tag, width, channels, offset, scale)
	if err != nil {
		return 0, nil, err
	}
	return rate, mono, nil
}

// framesToMono decodes interleaved frames and averages every channel.
func framesToMono(payload []byte, tag uint16, width, channels int, offset, scale float64) ([]float32, error) {
	stride := width * channels
	frames := len(payload) / stride
	mono := make([]float32, frames)
	for f := 0; f < frames; f++ {
		frame := payload[f*stride : (f+1)*stride]
		sum := 0.0
		for c := 0; c < channels; c++ {
			sample := frame[c*width : (c+1)*width]
			var value float64
			switch {
			case tag == 3 && width == 4:
				value = float64(float32frombits(binary.LittleEndian.Uint32(sample)))
			case tag == 3 && width == 8:
				value = math.Float64frombits(binary.LittleEndian.Uint64(sample))
			case tag == 1 && width == 1:
				value = (float64(sample[0]) - offset) / scale // unsigned 8-bit
			case tag == 1 && width == 2:
				value = (float64(int16(binary.LittleEndian.Uint16(sample))) - offset) / scale
			case tag == 1 && width == 3:
				v := int32(sample[0]) | int32(sample[1])<<8 | int32(sample[2])<<16
				if v&0x800000 != 0 {
					v |= ^int32(0xFFFFFF) // sign-extend 24-bit
				}
				value = (float64(v) - offset) / scale
			case tag == 1 && width == 4:
				value = (float64(int32(binary.LittleEndian.Uint32(sample))) - offset) / scale
			default:
				return nil, errWAV("not integer PCM or float WAV (tag %d, width %d)", tag, width)
			}
			sum += value
		}
		mono[f] = float32(sum / float64(channels))
	}
	return mono, nil
}

// Options normalizes the language and keyword options shared by the
// speech and vision APIs. keywords is nil, a string (used verbatim, one
// phrase per line) or a slice of phrases joined with newlines. Empty
// values stay empty so the engine receives NULL pointers.
func Options(language string, keywords any) (string, string, error) {
	kw := ""
	switch v := keywords.(type) {
	case nil:
	case string:
		kw = v
	case []string:
		kw = strings.Join(v, "\n")
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return "", "", fmt.Errorf("cactus: keywords must be strings, got %T", item)
			}
			parts = append(parts, s)
		}
		kw = strings.Join(parts, "\n")
	default:
		return "", "", fmt.Errorf("cactus: keywords must be a string or a list of strings, got %T", keywords)
	}
	return language, kw, nil
}
